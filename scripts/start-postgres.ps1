param(
    [string]$BaseUrl = "http://127.0.0.1:8081",
    [int]$TimeoutSeconds = 90
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

Push-Location $repoRoot
try {
    docker compose -p graphql-service --profile postgres up -d --build postgres app-postgres
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose failed with exit code $LASTEXITCODE"
    }

    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-WebRequest -Uri "$BaseUrl/healthz" -UseBasicParsing -TimeoutSec 2
            if ($response.StatusCode -eq 200) {
                Write-Host "PostgreSQL profile is ready at $BaseUrl"
                exit 0
            }
        }
        catch {
            Start-Sleep -Milliseconds 500
        }
    }

    docker compose -p graphql-service --profile postgres logs --tail 100 postgres app-postgres
    throw "Service did not become ready within $TimeoutSeconds seconds"
}
finally {
    Pop-Location
}
