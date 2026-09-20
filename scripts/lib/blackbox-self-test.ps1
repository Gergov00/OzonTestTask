$ErrorActionPreference = 'Stop'

$modulePath = Join-Path $PSScriptRoot 'blackbox-common.psm1'
Import-Module $modulePath -Force

function Expect-Throws {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [Parameter(Mandatory)][string]$MessageFragment
    )

    try {
        & $Action
    }
    catch {
        if ($_.Exception.Message -notlike "*$MessageFragment*") {
            throw "Expected error containing '$MessageFragment', got '$($_.Exception.Message)'"
        }
        return
    }

    throw "Expected an exception containing '$MessageFragment'"
}

Assert-True -Condition $true -Message 'true is accepted'
Expect-Throws -Action { Assert-True -Condition $false -Message 'sentinel failure' } -MessageFragment 'sentinel failure'

Assert-Equal -Expected 'alpha' -Actual 'alpha' -Message 'equal strings'
Expect-Throws -Action { Assert-Equal -Expected 1 -Actual 2 -Message 'numbers differ' } -MessageFragment 'Expected: 1; Actual: 2'

$body = New-GraphQLBody -Query 'query($id: ID!) { post(id: $id) { id } }' -Variables @{ id = '123' }
$decoded = $body | ConvertFrom-Json
Assert-Equal -Expected '123' -Actual $decoded.variables.id -Message 'variables are encoded'

$response = [pscustomobject]@{
    errors = @([pscustomobject]@{
        message = 'forbidden'
        extensions = [pscustomobject]@{ code = 'FORBIDDEN' }
    })
}
Assert-Equal -Expected 'FORBIDDEN' -Actual (Get-GraphQLErrorCode -Response $response) -Message 'GraphQL code is extracted'

Assert-Equal -Expected 'ws://127.0.0.1:8081/query' -Actual (ConvertTo-WebSocketUrl -BaseUrl 'http://127.0.0.1:8081') -Message 'HTTP becomes WS'
Assert-Equal -Expected 'wss://example.test/root/query' -Actual (ConvertTo-WebSocketUrl -BaseUrl 'https://example.test/root/') -Message 'HTTPS becomes WSS'

'blackbox helper self-test: PASS'
