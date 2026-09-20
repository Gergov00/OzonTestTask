$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Command
    )

    Write-Host "==> $Name"
    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed with exit code $LASTEXITCODE"
    }
}

Push-Location $repoRoot
try {
    Invoke-Checked "Generate" { go generate ./... }
    Invoke-Checked "Verify modules" { go mod verify }
    Invoke-Checked "Vet" { go vet ./... }
    Invoke-Checked "Tests" { go test -count=1 ./... }
    Invoke-Checked "Race tests" { go test -race -count=1 ./... }
    Write-Host "All checks passed"
}
finally {
    Pop-Location
}
