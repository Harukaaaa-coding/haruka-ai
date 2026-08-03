[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BackupDirectory,

    [Parameter()]
    [string]$ComposeFile,

    [Parameter()]
    [string]$ProjectName,

    [switch]$UseWSL,

    [Parameter()]
    [string]$WSLDistro = "Ubuntu-24.04",

    [switch]$SkipUploads,

    [switch]$SkipRedis,

    [switch]$ApplicationStopped,

    [switch]$ConfirmRestore
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if (-not $ConfirmRestore) {
    throw "Restore is destructive. Re-run with -ConfirmRestore only after verifying the backup and target."
}
if (-not $ApplicationStopped) {
    throw "Stop every GopherAI API/worker process first, then re-run with -ApplicationStopped."
}

function Resolve-FullPath {
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][string]$BasePath)
    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path -Path $BasePath -ChildPath $Path))
}

$scriptDirectory = [System.IO.Path]::GetFullPath($PSScriptRoot)
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path -Path $scriptDirectory -ChildPath ".."))
$BackupDirectory = Resolve-FullPath -Path $BackupDirectory -BasePath $projectRoot
if (-not (Test-Path -LiteralPath $BackupDirectory -PathType Container)) {
    throw "Backup directory was not found: $BackupDirectory"
}
$manifestPath = Join-Path -Path $BackupDirectory -ChildPath "manifest.json"
$mysqlDumpPath = Join-Path -Path $BackupDirectory -ChildPath "mysql.sql"
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf) -or -not (Test-Path -LiteralPath $mysqlDumpPath -PathType Leaf)) {
    throw "Backup is incomplete: manifest.json and mysql.sql are required."
}
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
if ($manifest.Format -ne "gopherai-backup-v1") {
    throw "Backup manifest format is not supported."
}
foreach ($file in $manifest.Files) {
    $path = Join-Path -Path $BackupDirectory -ChildPath $file.Name
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Backup file is missing: $($file.Name)"
    }
    $actualHash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
    if ($actualHash -ne $file.SHA256) {
        throw "Backup checksum mismatch: $($file.Name)"
    }
}

if ([string]::IsNullOrWhiteSpace($ComposeFile)) {
    $ComposeFile = Join-Path -Path $projectRoot -ChildPath "docker-compose.yml"
}
$ComposeFile = Resolve-FullPath -Path $ComposeFile -BasePath $projectRoot
if (-not (Test-Path -LiteralPath $ComposeFile -PathType Leaf)) {
    throw "Compose file was not found: $ComposeFile"
}

function Convert-ToDockerPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not $UseWSL) { return $Path }
    $converted = & wsl.exe -d $WSLDistro -- wslpath -a $Path
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($converted)) {
        throw "Could not convert path for WSL Docker: $Path"
    }
    return $converted.Trim()
}

if (-not $UseWSL) {
    $docker = Get-Command -Name docker -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $docker) { throw "Docker was not found on PATH. Use -UseWSL when Docker only runs in WSL." }
    $dockerExecutable = $docker.Source
} elseif ($null -eq (Get-Command -Name wsl.exe -CommandType Application -ErrorAction SilentlyContinue)) {
    throw "wsl.exe was not found on PATH."
}

$composeFileForDocker = Convert-ToDockerPath -Path $ComposeFile
$composePrefix = @("compose", "-f", $composeFileForDocker)
if (-not [string]::IsNullOrWhiteSpace($ProjectName)) { $composePrefix += @("-p", $ProjectName) }
function Invoke-Compose {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)
    if ($UseWSL) {
        & wsl.exe -d $WSLDistro -- docker @composePrefix @Arguments
    } else {
        & $dockerExecutable @composePrefix @Arguments
    }
    if ($LASTEXITCODE -ne 0) { throw "docker compose command failed (exit code $LASTEXITCODE)." }
}

$backupDirectoryForDocker = Convert-ToDockerPath -Path $BackupDirectory
$remoteMySQLDump = "/tmp/gopherai-restore-" + [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ") + ".sql"

try {
    Invoke-Compose -Arguments @("cp", "$backupDirectoryForDocker/mysql.sql", "mysql:$remoteMySQLDump")
    $restoreCommand = 'exec mysql -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" "$MYSQL_DATABASE" < ' + $remoteMySQLDump
    Invoke-Compose -Arguments @("exec", "-T", "mysql", "sh", "-ec", $restoreCommand)

    $redisDumpPath = Join-Path -Path $BackupDirectory -ChildPath "redis.rdb"
    if (-not $SkipRedis -and (Test-Path -LiteralPath $redisDumpPath -PathType Leaf)) {
        # A physical RDB replacement requires Redis to be stopped. docker cp
        # supports stopped containers; startup below validates the replacement.
        Invoke-Compose -Arguments @("stop", "redis")
        Invoke-Compose -Arguments @("cp", "$backupDirectoryForDocker/redis.rdb", "redis:/data/dump.rdb")
        Invoke-Compose -Arguments @("start", "redis")
    }

    $uploadsArchivePath = Join-Path -Path $BackupDirectory -ChildPath "uploads.zip"
    if (-not $SkipUploads -and (Test-Path -LiteralPath $uploadsArchivePath -PathType Leaf)) {
        $stagingDirectory = Join-Path -Path $projectRoot -ChildPath (".restore-staging-" + [Guid]::NewGuid().ToString("N"))
        Expand-Archive -LiteralPath $uploadsArchivePath -DestinationPath $stagingDirectory -Force
        $stagedUploads = Join-Path -Path $stagingDirectory -ChildPath "uploads"
        if (-not (Test-Path -LiteralPath $stagedUploads -PathType Container)) {
            throw "uploads.zip does not contain the expected uploads directory."
        }
        $uploadsDirectory = Join-Path -Path $projectRoot -ChildPath "uploads"
        if (Test-Path -LiteralPath $uploadsDirectory -PathType Container) {
            $preservedUploads = $uploadsDirectory + ".pre-restore-" + [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
            Move-Item -LiteralPath $uploadsDirectory -Destination $preservedUploads
            Write-Warning "Previous uploads were preserved at $preservedUploads"
        }
        Move-Item -LiteralPath $stagedUploads -Destination $uploadsDirectory
    }

    Write-Host "Restore complete. Run: go run ./cmd/migrate -command verify"
    Write-Host "Then start the API and verify /readyz before allowing traffic."
} finally {
    try {
        Invoke-Compose -Arguments @("exec", "-T", "mysql", "rm", "-f", $remoteMySQLDump)
    } catch {
        Write-Warning "Could not remove the temporary MySQL restore file: $remoteMySQLDump"
    }
}
