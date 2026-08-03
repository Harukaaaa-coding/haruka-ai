[CmdletBinding()]
param(
    [Parameter()]
    [ValidateNotNullOrEmpty()]
    [string]$Address = "127.0.0.1:8081"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

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

$scriptDirectory = [System.IO.Path]::GetFullPath($PSScriptRoot)
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path -Path $scriptDirectory -ChildPath ".."))
$mcpDirectory = Join-Path -Path $projectRoot -ChildPath "common\mcp"
$mcpGoModPath = Join-Path -Path $mcpDirectory -ChildPath "go.mod"

if ([string]::IsNullOrWhiteSpace($Address)) {
    throw "Address cannot be empty. Example: 127.0.0.1:8081"
}

Assert-RequiredFile -Path $mcpGoModPath -Description "MCP Go module file"

$goCommand = Get-Command -Name "go" -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -eq $goCommand) {
    throw "Go was not found on PATH. Install Go or add go.exe to the current process PATH."
}

Write-Host "Starting MCP server at $Address."
Push-Location -LiteralPath $mcpDirectory
try {
    & $goCommand.Source "run" "." "-mode" "server" "-http-addr" $Address
    $processExitCode = $LASTEXITCODE
} finally {
    Pop-Location
}

if ($processExitCode -ne 0) {
    exit $processExitCode
}
