[CmdletBinding()]
param(
    [Parameter()]
    [string]$BackupRoot,

    [Parameter()]
    [string]$ComposeFile,

    [Parameter()]
    [string]$ProjectName,

    [switch]$UseWSL,

    [Parameter()]
    [string]$WSLDistro = "Ubuntu-24.04",

    [switch]$SkipUploads,

    [switch]$SkipRedis
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Resolve-FullPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [string]$BasePath
    )

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path -Path $BasePath -ChildPath $Path))
}

$scriptDirectory = [System.IO.Path]::GetFullPath($PSScriptRoot)
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path -Path $scriptDirectory -ChildPath ".."))
if ([string]::IsNullOrWhiteSpace($ComposeFile)) {
    $ComposeFile = Join-Path -Path $projectRoot -ChildPath "docker-compose.yml"
}
$ComposeFile = Resolve-FullPath -Path $ComposeFile -BasePath $projectRoot
if (-not (Test-Path -LiteralPath $ComposeFile -PathType Leaf)) {
    throw "Compose file was not found: $ComposeFile"
}
if ([string]::IsNullOrWhiteSpace($BackupRoot)) {
    $BackupRoot = Join-Path -Path $projectRoot -ChildPath "backups"
}
$BackupRoot = Resolve-FullPath -Path $BackupRoot -BasePath $projectRoot

function Convert-ToDockerPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not $UseWSL) {
        return $Path
    }
    $converted = & wsl.exe -d $WSLDistro -- wslpath -a $Path
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($converted)) {
        throw "Could not convert path for WSL Docker: $Path"
    }
    return $converted.Trim()
}

if (-not $UseWSL) {
    $docker = Get-Command -Name docker -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $docker) {
        throw "Docker was not found on PATH. Use -UseWSL when Docker only runs in WSL."
    }
    $dockerExecutable = $docker.Source
} elseif ($null -eq (Get-Command -Name wsl.exe -CommandType Application -ErrorAction SilentlyContinue)) {
    throw "wsl.exe was not found on PATH."
}

$composeFileForDocker = Convert-ToDockerPath -Path $ComposeFile
$composePrefix = @("compose", "-f", $composeFileForDocker)
if (-not [string]::IsNullOrWhiteSpace($ProjectName)) {
    $composePrefix += @("-p", $ProjectName)
}

function Invoke-Compose {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)

    if ($UseWSL) {
        & wsl.exe -d $WSLDistro -- docker @composePrefix @Arguments
    } else {
        & $dockerExecutable @composePrefix @Arguments
    }
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose command failed (exit code $LASTEXITCODE)."
    }
}

function Assert-ComposeService {
    param([Parameter(Mandatory = $true)][string]$Service)

    if ($UseWSL) {
        $containerID = & wsl.exe -d $WSLDistro -- docker @composePrefix ps -q $Service
    } else {
        $containerID = & $dockerExecutable @composePrefix ps -q $Service
    }
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($containerID)) {
        throw "Compose service '$Service' is not running. Start the infrastructure before backing it up."
    }
}

Assert-ComposeService -Service "mysql"
if (-not $SkipRedis) {
    Assert-ComposeService -Service "redis"
}

[System.IO.Directory]::CreateDirectory($BackupRoot) | Out-Null
$timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
$backupDirectory = Join-Path -Path $BackupRoot -ChildPath $timestamp
[System.IO.Directory]::CreateDirectory($backupDirectory) | Out-Null
$backupDirectoryForDocker = Convert-ToDockerPath -Path $backupDirectory

$mysqlDumpPath = Join-Path -Path $backupDirectory -ChildPath "mysql.sql"
$redisDumpPath = Join-Path -Path $backupDirectory -ChildPath "redis.rdb"
$uploadsArchivePath = Join-Path -Path $backupDirectory -ChildPath "uploads.zip"
$remoteMySQLDump = "/tmp/gopherai-backup-$timestamp.sql"

try {
    # Write the SQL inside the MySQL container, then copy it as raw bytes. This
    # avoids PowerShell output encodings corrupting a dump with Chinese text.
    $dumpCommand = 'exec mysqldump --single-transaction --quick --routines --events --no-tablespaces --set-gtid-purged=OFF -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" "$MYSQL_DATABASE" > ' + $remoteMySQLDump
    Invoke-Compose -Arguments @("exec", "-T", "mysql", "sh", "-ec", $dumpCommand)
    Invoke-Compose -Arguments @("cp", "mysql:$remoteMySQLDump", $backupDirectoryForDocker)
    $copiedMySQLDump = Join-Path -Path $backupDirectory -ChildPath ([System.IO.Path]::GetFileName($remoteMySQLDump))
    if (-not (Test-Path -LiteralPath $copiedMySQLDump -PathType Leaf)) {
        throw "MySQL dump was not copied from the container."
    }
    Move-Item -LiteralPath $copiedMySQLDump -Destination $mysqlDumpPath

    if (-not $SkipRedis) {
        # SAVE produces a consistent RDB snapshot. It can briefly block Redis,
        # so schedule this operation outside peak traffic for production.
        Invoke-Compose -Arguments @("exec", "-T", "redis", "sh", "-ec", 'exec redis-cli --no-auth-warning -a "$REDIS_PASSWORD" SAVE')
        Invoke-Compose -Arguments @("cp", "redis:/data/dump.rdb", $backupDirectoryForDocker)
        $copiedRedisDump = Join-Path -Path $backupDirectory -ChildPath "dump.rdb"
        if (-not (Test-Path -LiteralPath $copiedRedisDump -PathType Leaf)) {
            throw "Redis RDB was not copied from the container."
        }
        Move-Item -LiteralPath $copiedRedisDump -Destination $redisDumpPath
    }

    if (-not $SkipUploads) {
        $uploadsDirectory = Join-Path -Path $projectRoot -ChildPath "uploads"
        if (Test-Path -LiteralPath $uploadsDirectory -PathType Container) {
            Compress-Archive -LiteralPath $uploadsDirectory -DestinationPath $uploadsArchivePath -CompressionLevel Optimal
        } else {
            Set-Content -LiteralPath (Join-Path -Path $backupDirectory -ChildPath "uploads.absent") -Value "uploads directory was absent at backup time" -Encoding utf8
        }
    }

    $files = Get-ChildItem -LiteralPath $backupDirectory -File | ForEach-Object {
        [PSCustomObject]@{
            Name = $_.Name
            Bytes = $_.Length
            SHA256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
        }
    }
    [PSCustomObject]@{
        Format = "gopherai-backup-v1"
        CreatedAtUTC = [DateTime]::UtcNow.ToString("o")
        ComposeFile = [System.IO.Path]::GetFileName($ComposeFile)
        IncludesRedis = -not $SkipRedis
        IncludesUploads = -not $SkipUploads
        Files = $files
    } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path -Path $backupDirectory -ChildPath "manifest.json") -Encoding utf8

    Write-Host "Backup complete: $backupDirectory"
    Write-Host "Before relying on this backup, restore it into an isolated environment and run cmd/migrate -command verify."
} finally {
    # Best effort only: an interrupted dump must not leave its secret-free SQL
    # artifact in a long-lived MySQL container.
    try {
        Invoke-Compose -Arguments @("exec", "-T", "mysql", "rm", "-f", $remoteMySQLDump)
    } catch {
        Write-Warning "Could not remove the temporary MySQL dump from the container. Remove it manually: $remoteMySQLDump"
    }
}
