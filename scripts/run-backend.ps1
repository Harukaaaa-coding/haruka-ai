[CmdletBinding()]
param(
    [switch]$WithoutOnnx,
    [switch]$SkipMigrations
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Import-DotEnv {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return
    }

    $lineNumber = 0
    foreach ($rawLine in [System.IO.File]::ReadLines($Path)) {
        $lineNumber++
        $line = $rawLine.Trim()
        if ([string]::IsNullOrWhiteSpace($line) -or $line.StartsWith("#")) {
            continue
        }

        $match = [regex]::Match($line, "^(?<key>[A-Za-z_][A-Za-z0-9_]*)=(?<value>.*)$")
        if (-not $match.Success) {
            throw "Invalid .env entry at line $lineNumber. Expected KEY=VALUE with a valid variable name."
        }

        $key = $match.Groups["key"].Value
        $value = $match.Groups["value"].Value.Trim()
        if ($value.Length -ge 2) {
            $firstCharacter = $value[0]
            $lastCharacter = $value[$value.Length - 1]
            if (($firstCharacter -eq '"' -and $lastCharacter -eq '"') -or
                ($firstCharacter -eq "'" -and $lastCharacter -eq "'")) {
                $value = $value.Substring(1, $value.Length - 2)
            }
        }

        [Environment]::SetEnvironmentVariable($key, $value, "Process")
    }

    Write-Host "Loaded process environment from .env (values hidden)."
}

function Resolve-ProjectPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Value,

        [Parameter(Mandatory = $true)]
        [string]$ProjectRoot
    )

    if ([System.IO.Path]::IsPathRooted($Value)) {
        return [System.IO.Path]::GetFullPath($Value)
    }

    return [System.IO.Path]::GetFullPath((Join-Path -Path $ProjectRoot -ChildPath $Value))
}

function Assert-RequiredFile {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [string]$Description
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Description was not found: $Path"
    }
}

function Get-AssetPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$EnvironmentName,

        [Parameter(Mandatory = $true)]
        [string]$DefaultRelativePath,

        [Parameter(Mandatory = $true)]
        [string]$ProjectRoot
    )

    $configuredValue = [Environment]::GetEnvironmentVariable($EnvironmentName, "Process")
    if ([string]::IsNullOrWhiteSpace($configuredValue)) {
        $configuredValue = $DefaultRelativePath
    }

    $resolvedPath = Resolve-ProjectPath -Value $configuredValue -ProjectRoot $ProjectRoot
    [Environment]::SetEnvironmentVariable($EnvironmentName, $resolvedPath, "Process")
    return $resolvedPath
}

function Add-ProcessPathEntry {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Directory
    )

    $currentPath = [Environment]::GetEnvironmentVariable("PATH", "Process")
    $pathSeparator = [System.IO.Path]::PathSeparator
    $entries = @()
    if (-not [string]::IsNullOrWhiteSpace($currentPath)) {
        $entries = $currentPath -split [regex]::Escape([string]$pathSeparator)
    }

    $normalizedDirectory = [System.IO.Path]::GetFullPath($Directory).TrimEnd('\', '/')
    $alreadyPresent = $entries | Where-Object {
        if ([string]::IsNullOrWhiteSpace($_)) {
            return $false
        }

        try {
            $normalizedEntry = [System.IO.Path]::GetFullPath($_.Trim('"')).TrimEnd('\', '/')
            return [string]::Equals(
                $normalizedEntry,
                $normalizedDirectory,
                [System.StringComparison]::OrdinalIgnoreCase
            )
        } catch {
            return $false
        }
    }
    if (-not $alreadyPresent) {
        $updatedPath = if ([string]::IsNullOrWhiteSpace($currentPath)) {
            $Directory
        } else {
            $Directory + $pathSeparator + $currentPath
        }
        [Environment]::SetEnvironmentVariable("PATH", $updatedPath, "Process")
    }
}

$scriptDirectory = [System.IO.Path]::GetFullPath($PSScriptRoot)
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path -Path $scriptDirectory -ChildPath ".."))
$envFile = Join-Path -Path $projectRoot -ChildPath ".env"

Import-DotEnv -Path $envFile

$goModPath = Join-Path -Path $projectRoot -ChildPath "go.mod"
Assert-RequiredFile -Path $goModPath -Description "Go module file"

$configuredConfigPath = [Environment]::GetEnvironmentVariable("GOPHERAI_CONFIG_PATH", "Process")
if ([string]::IsNullOrWhiteSpace($configuredConfigPath)) {
    $configPath = Join-Path -Path $projectRoot -ChildPath "config\config.toml"
} else {
    $configPath = Resolve-ProjectPath -Value $configuredConfigPath -ProjectRoot $projectRoot
    [Environment]::SetEnvironmentVariable("GOPHERAI_CONFIG_PATH", $configPath, "Process")
}
Assert-RequiredFile -Path $configPath -Description "Backend configuration file"

$goCommand = Get-Command -Name "go" -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -eq $goCommand) {
    throw "Go was not found on PATH. Install Go or add go.exe to the current process PATH."
}

if (-not $SkipMigrations) {
    # Migrations are an explicit release step, even for the convenient local
    # launcher. The API itself starts in verify mode and will refuse a stale
    # schema if an operator intentionally uses -SkipMigrations.
    Write-Host "Applying database migrations."
    Push-Location -LiteralPath $projectRoot
    try {
        & $goCommand.Source "run" "-buildvcs=false" "./cmd/migrate" "-command" "up"
        $migrationExitCode = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($migrationExitCode -ne 0) {
        throw "Database migration failed (exit code $migrationExitCode)."
    }
}

$goArguments = @("run", "-buildvcs=false", ".")
if (-not $WithoutOnnx) {
    $modelPath = Get-AssetPath `
        -EnvironmentName "GOPHERAI_ONNX_MODEL_PATH" `
        -DefaultRelativePath "models\mobilenetv2\mobilenetv2-7.onnx" `
        -ProjectRoot $projectRoot
    $labelPath = Get-AssetPath `
        -EnvironmentName "GOPHERAI_ONNX_LABEL_PATH" `
        -DefaultRelativePath "models\mobilenetv2\synset.txt" `
        -ProjectRoot $projectRoot
    $runtimePath = Get-AssetPath `
        -EnvironmentName "GOPHERAI_ONNX_RUNTIME_PATH" `
        -DefaultRelativePath "runtime\onnxruntime-win-x64-1.22.0\lib\onnxruntime.dll" `
        -ProjectRoot $projectRoot

    $compilerDirectory = Join-Path -Path $projectRoot -ChildPath "tools\w64devkit\bin"
    $compilerPath = Join-Path -Path $compilerDirectory -ChildPath "gcc.exe"
    $objcopyPath = Join-Path -Path $compilerDirectory -ChildPath "objcopy.exe"
    $objdumpPath = Join-Path -Path $compilerDirectory -ChildPath "objdump.exe"
    $wrapperSourcePath = Join-Path -Path $projectRoot -ChildPath "cmd\cgo-gcc-wrapper\main.go"
    $cacheDirectory = Join-Path -Path $projectRoot -ChildPath ".cache"
    $wrapperPath = Join-Path -Path $cacheDirectory -ChildPath "cgo-gcc-wrapper.exe"

    Assert-RequiredFile -Path $modelPath -Description "MobileNetV2 ONNX model"
    Assert-RequiredFile -Path $labelPath -Description "ImageNet label file"
    Assert-RequiredFile -Path $runtimePath -Description "ONNX Runtime DLL"
    Assert-RequiredFile -Path $compilerPath -Description "MinGW-w64 GCC compiler"
    Assert-RequiredFile -Path $objcopyPath -Description "MinGW-w64 objcopy"
    Assert-RequiredFile -Path $objdumpPath -Description "MinGW-w64 objdump"
    Assert-RequiredFile -Path $wrapperSourcePath -Description "cgo GCC wrapper source"

    Add-ProcessPathEntry -Directory (Split-Path -Parent $runtimePath)
    Add-ProcessPathEntry -Directory $compilerDirectory

    [System.IO.Directory]::CreateDirectory($cacheDirectory) | Out-Null
    [Environment]::SetEnvironmentVariable("CGO_ENABLED", "0", "Process")
    Push-Location -LiteralPath $projectRoot
    try {
        & $goCommand.Source build -buildvcs=false -o $wrapperPath ./cmd/cgo-gcc-wrapper
        $wrapperBuildExitCode = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($wrapperBuildExitCode -ne 0) {
        throw "Failed to build the cgo GCC compatibility wrapper (exit code $wrapperBuildExitCode)."
    }
    Assert-RequiredFile -Path $wrapperPath -Description "cgo GCC compatibility wrapper"

    [Environment]::SetEnvironmentVariable("GOPHERAI_REAL_GCC", $compilerPath, "Process")
    [Environment]::SetEnvironmentVariable("GOPHERAI_OBJCOPY", $objcopyPath, "Process")
    [Environment]::SetEnvironmentVariable("GOPHERAI_OBJDUMP", $objdumpPath, "Process")
    [Environment]::SetEnvironmentVariable("CGO_ENABLED", "1", "Process")
    [Environment]::SetEnvironmentVariable("CC", $wrapperPath, "Process")

    $goArguments = @("run", "-buildvcs=false", "-tags", "onnx", ".")
    Write-Host "Starting backend with ONNX image recognition enabled."
} else {
    Write-Host "Starting backend without ONNX image recognition."
}

Push-Location -LiteralPath $projectRoot
try {
    & $goCommand.Source @goArguments
    $processExitCode = $LASTEXITCODE
} finally {
    Pop-Location
}

if ($processExitCode -ne 0) {
    exit $processExitCode
}
