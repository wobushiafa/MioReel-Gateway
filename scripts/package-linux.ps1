param(
    [string[]]$Arch = @("amd64"),
    [string]$Version = "1.0.0",
    [string]$OutputDir = "dist",
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$ServerDir = Join-Path $RepoRoot "server"
$OutPath = if ([System.IO.Path]::IsPathRooted($OutputDir)) { $OutputDir } else { Join-Path $RepoRoot $OutputDir }

if ($Arch.Count -eq 1 -and $Arch[0] -eq "all") {
    $Arch = @("amd64", "arm64")
}

function Copy-WithLF {
    param([string]$Source, [string]$Dest)
    $parent = Split-Path -Parent $Dest
    if (-not (Test-Path $parent)) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
    $text = [System.IO.File]::ReadAllText($Source, [System.Text.UTF8Encoding]::new($false))
    $lf = $text -replace "`r`n", "`n"
    [System.IO.File]::WriteAllText($Dest, $lf, [System.Text.UTF8Encoding]::new($false))
}

if (-not $SkipTests) {
    Write-Host "==> Running go vet & go test..."
    Push-Location $ServerDir
    try {
        & go vet ./...
        if ($LASTEXITCODE -ne 0) { throw "go vet failed" }
        & go test -count=1 ./...
        if ($LASTEXITCODE -ne 0) { throw "go test failed" }
    }
    finally {
        Pop-Location
    }
}

New-Item -ItemType Directory -Force -Path $OutPath | Out-Null
$checksums = @()

foreach ($a in $Arch) {
    $pkgName = "media-gateway-$Version-linux-$a"
    $stageParent = Join-Path ([System.IO.Path]::GetTempPath()) ("mioreel_pkg_" + [guid]::NewGuid().ToString("N"))
    $stageDir = Join-Path $stageParent $pkgName
    New-Item -ItemType Directory -Force -Path $stageDir | Out-Null

    try {
        Write-Host "==> Building linux/$a binary..."
        $binTarget = Join-Path $stageDir "media-gateway"

        Push-Location $ServerDir
        $oldCgo = $env:CGO_ENABLED
        $oldOs = $env:GOOS
        $oldArch = $env:GOARCH
        try {
            $env:CGO_ENABLED = "0"
            $env:GOOS = "linux"
            $env:GOARCH = $a
            & go build -trimpath -ldflags "-s -w" -o $binTarget ./cmd/gateway
            if ($LASTEXITCODE -ne 0) { throw "go build linux/$a failed" }
        }
        finally {
            $env:CGO_ENABLED = $oldCgo
            $env:GOOS = $oldOs
            $env:GOARCH = $oldArch
            Pop-Location
        }

        Copy-WithLF (Join-Path $RepoRoot "configs\gateway.example.json") (Join-Path $stageDir "configs\gateway.example.json")
        Copy-WithLF (Join-Path $RepoRoot "deploy\systemd\media-gateway.service") (Join-Path $stageDir "deploy\media-gateway.service")
        Copy-WithLF (Join-Path $RepoRoot "deploy\scripts\install.sh") (Join-Path $stageDir "deploy\install.sh")
        Copy-WithLF (Join-Path $RepoRoot "deploy\scripts\start.sh") (Join-Path $stageDir "start.sh")
        Copy-WithLF (Join-Path $RepoRoot "deploy\scripts\stop.sh") (Join-Path $stageDir "stop.sh")
        New-Item -ItemType Directory -Force -Path (Join-Path $stageDir "data") | Out-Null
        [System.IO.File]::WriteAllText((Join-Path $stageDir "data\.gitkeep"), "", [System.Text.UTF8Encoding]::new($false))

        $tarName = "$pkgName.tar.gz"
        $tarPath = Join-Path $OutPath $tarName
        if (Test-Path $tarPath) { Remove-Item -Force $tarPath }

        Write-Host "==> Packing $tarPath..."
        & go run (Join-Path $PSScriptRoot "pack_targz.go") $stageParent $pkgName $tarPath
        if ($LASTEXITCODE -ne 0) { throw "pack_targz failed for $tarName" }

        $hash = (Get-FileHash -Path $tarPath -Algorithm SHA256).Hash.ToLowerInvariant()
        $checksums += "$hash  $tarName"
    }
    finally {
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $stageParent
    }
}

$sumPath = Join-Path $OutPath "SHA256SUMS.txt"
[System.IO.File]::WriteAllLines($sumPath, $checksums, [System.Text.UTF8Encoding]::new($false))

Write-Host "==> Done! Artifacts in $OutPath`:"
Get-Content $sumPath | ForEach-Object { Write-Host "  $_" }
