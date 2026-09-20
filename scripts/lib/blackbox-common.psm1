Set-StrictMode -Version Latest

function Assert-True {
    param(
        [Parameter(Mandatory)][bool]$Condition,
        [Parameter(Mandatory)][string]$Message
    )

    if (-not $Condition) {
        throw $Message
    }
}

function Assert-Equal {
    param(
        [AllowNull()]$Expected,
        [AllowNull()]$Actual,
        [Parameter(Mandatory)][string]$Message
    )

    if ($Expected -ne $Actual) {
        throw "$Message. Expected: $Expected; Actual: $Actual"
    }
}

function New-GraphQLBody {
    param(
        [Parameter(Mandatory)][string]$Query,
        [hashtable]$Variables = @{}
    )

    return @{ query = $Query; variables = $Variables } | ConvertTo-Json -Depth 20 -Compress
}

function Get-GraphQLErrorCode {
    param([Parameter(Mandatory)]$Response)

    if ($null -eq $Response.errors -or @($Response.errors).Count -eq 0) {
        return $null
    }
    return $Response.errors[0].extensions.code
}

function ConvertTo-WebSocketUrl {
    param([Parameter(Mandatory)][string]$BaseUrl)

    $uri = [Uri]$BaseUrl
    $builder = [UriBuilder]::new($uri)
    $builder.Scheme = if ($uri.Scheme -eq 'https') { 'wss' } else { 'ws' }
    $builder.Port = $uri.Port
    $builder.Path = ($uri.AbsolutePath.TrimEnd('/') + '/query')
    return $builder.Uri.AbsoluteUri.TrimEnd('/')
}

Export-ModuleMember -Function Assert-True, Assert-Equal, New-GraphQLBody, Get-GraphQLErrorCode, ConvertTo-WebSocketUrl
