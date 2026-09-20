Set-StrictMode -Version Latest

function New-TimeoutTokenSource {
    param([Parameter(Mandatory)][int]$TimeoutMs)

    $source = [Threading.CancellationTokenSource]::new()
    $source.CancelAfter($TimeoutMs)
    return $source
}

function Send-WebSocketText {
    param(
        [Parameter(Mandatory)][hashtable]$State,
        [Parameter(Mandatory)][string]$Text,
        [int]$TimeoutMs = 5000
    )

    $bytes = [Text.Encoding]::UTF8.GetBytes($Text)
    $segment = [ArraySegment[byte]]::new($bytes)
    $timeout = New-TimeoutTokenSource -TimeoutMs $TimeoutMs
    try {
        [void]$State.Client.SendAsync(
            $segment,
            [Net.WebSockets.WebSocketMessageType]::Text,
            $true,
            $timeout.Token
        ).GetAwaiter().GetResult()
    }
    finally {
        $timeout.Dispose()
    }
}

function Send-WebSocketJson {
    param(
        [Parameter(Mandatory)][hashtable]$State,
        [Parameter(Mandatory)]$Message,
        [int]$TimeoutMs = 5000
    )

    Send-WebSocketText -State $State -Text ($Message | ConvertTo-Json -Depth 20 -Compress) -TimeoutMs $TimeoutMs
}

function Receive-WebSocketJson {
    param(
        [Parameter(Mandatory)][hashtable]$State,
        [int]$TimeoutMs = 5000,
        [switch]$AllowTimeout
    )

    $deadline = [DateTime]::UtcNow.AddMilliseconds($TimeoutMs)
    while ($true) {
        if ($null -eq $State.ReceiveTask) {
            $State.ReceiveBuffer = [byte[]]::new(4096)
            $segment = [ArraySegment[byte]]::new($State.ReceiveBuffer)
            $State.ReceiveCts = New-TimeoutTokenSource -TimeoutMs 10000
            $State.ReceiveTask = $State.Client.ReceiveAsync($segment, $State.ReceiveCts.Token)
        }

        $remaining = [int][Math]::Ceiling(($deadline - [DateTime]::UtcNow).TotalMilliseconds)
        if ($remaining -le 0 -or -not $State.ReceiveTask.Wait([Math]::Max(0, $remaining))) {
            if ($AllowTimeout) {
                return $null
            }
            throw "Timed out after $TimeoutMs ms waiting for a WebSocket frame"
        }

        try {
            $result = $State.ReceiveTask.GetAwaiter().GetResult()
        }
        finally {
            $State.ReceiveTask = $null
            $State.ReceiveCts.Dispose()
            $State.ReceiveCts = $null
        }
        if ($result.MessageType -eq [Net.WebSockets.WebSocketMessageType]::Close) {
            throw "WebSocket closed by server: $($State.Client.CloseStatus) $($State.Client.CloseStatusDescription)"
        }
        if ($result.MessageType -ne [Net.WebSockets.WebSocketMessageType]::Text) {
            throw "Unexpected WebSocket message type '$($result.MessageType)'"
        }
        if ($result.Count -gt 0) {
            $State.ReceiveStream.Write($State.ReceiveBuffer, 0, $result.Count)
        }
        if (-not $result.EndOfMessage) {
            continue
        }

        $text = [Text.Encoding]::UTF8.GetString($State.ReceiveStream.ToArray())
        $State.ReceiveStream.SetLength(0)
        $State.ReceiveStream.Position = 0
        return $text | ConvertFrom-Json -Depth 30
    }
}

function Receive-GraphQLWebSocketMessage {
    param(
        [Parameter(Mandatory)][hashtable]$State,
        [string]$OperationId,
        [string[]]$Types = @(),
        [int]$TimeoutMs = 5000,
        [switch]$AllowTimeout
    )

    $deadline = [DateTime]::UtcNow.AddMilliseconds($TimeoutMs)
    while ($true) {
        for ($index = 0; $index -lt $State.Pending.Count; $index++) {
            $candidate = $State.Pending[$index]
            $idMatches = [string]::IsNullOrEmpty($OperationId) -or $candidate.id -eq $OperationId
            $typeMatches = $Types.Count -eq 0 -or $candidate.type -in $Types
            if ($idMatches -and $typeMatches) {
                $State.Pending.RemoveAt($index)
                return $candidate
            }
        }

        $remaining = [int][Math]::Ceiling(($deadline - [DateTime]::UtcNow).TotalMilliseconds)
        if ($remaining -le 0) {
            if ($AllowTimeout) {
                return $null
            }
            throw "Timed out after $TimeoutMs ms waiting for GraphQL WebSocket message"
        }

        $message = Receive-WebSocketJson -State $State -TimeoutMs $remaining -AllowTimeout:$AllowTimeout
        if ($null -eq $message) {
            return $null
        }
        if ($message.type -eq 'ping') {
            $pong = [ordered]@{ type = 'pong' }
            if ($null -ne $message.PSObject.Properties['payload']) {
                $pong.payload = $message.payload
            }
            Send-WebSocketJson -State $State -Message $pong
            continue
        }
        if ($message.type -eq 'pong') {
            continue
        }

        $idMatches = [string]::IsNullOrEmpty($OperationId) -or $message.id -eq $OperationId
        $typeMatches = $Types.Count -eq 0 -or $message.type -in $Types
        if ($idMatches -and $typeMatches) {
            return $message
        }
        $State.Pending.Add($message)
    }
}

function Connect-GraphQLWebSocket {
    param(
        [Parameter(Mandatory)][string]$Uri,
        [string]$AuthorId,
        [int]$TimeoutMs = 5000
    )

    $client = [Net.WebSockets.ClientWebSocket]::new()
    $client.Options.AddSubProtocol('graphql-transport-ws')
    if (-not [string]::IsNullOrWhiteSpace($AuthorId)) {
        $client.Options.SetRequestHeader('X-Author-ID', $AuthorId)
    }
    $state = @{
        Client = $client
        Pending = [Collections.Generic.List[object]]::new()
        ReceiveTask = $null
        ReceiveCts = $null
        ReceiveBuffer = $null
        ReceiveStream = [IO.MemoryStream]::new()
    }
    $timeout = New-TimeoutTokenSource -TimeoutMs $TimeoutMs
    try {
        [void]$client.ConnectAsync([Uri]$Uri, $timeout.Token).GetAwaiter().GetResult()
    }
    finally {
        $timeout.Dispose()
    }

    if ($client.SubProtocol -ne 'graphql-transport-ws') {
        $client.Dispose()
        throw "Server negotiated unexpected WebSocket subprotocol '$($client.SubProtocol)'"
    }

    Send-WebSocketJson -State $state -Message @{ type = 'connection_init' } -TimeoutMs $TimeoutMs
    $ack = Receive-GraphQLWebSocketMessage -State $state -Types @('connection_ack', 'error') -TimeoutMs $TimeoutMs
    if ($ack.type -ne 'connection_ack') {
        $client.Dispose()
        throw "WebSocket initialization failed: $($ack | ConvertTo-Json -Depth 20 -Compress)"
    }
    return $state
}

function Start-GraphQLWebSocketOperation {
    param(
        [Parameter(Mandatory)][hashtable]$State,
        [Parameter(Mandatory)][string]$OperationId,
        [Parameter(Mandatory)][string]$Query,
        [hashtable]$Variables = @{}
    )

    Send-WebSocketJson -State $State -Message @{
        id = $OperationId
        type = 'subscribe'
        payload = @{ query = $Query; variables = $Variables }
    }
}

function Stop-GraphQLWebSocketOperation {
    param(
        [Parameter(Mandatory)][hashtable]$State,
        [Parameter(Mandatory)][string]$OperationId
    )

    Send-WebSocketJson -State $State -Message @{ id = $OperationId; type = 'complete' }
}

function Close-GraphQLWebSocket {
    param(
        [AllowNull()][hashtable]$State,
        [int]$TimeoutMs = 2000
    )

    if ($null -eq $State) {
        return
    }
    try {
        if ($State.Client.State -eq [Net.WebSockets.WebSocketState]::Open) {
            if ($null -ne $State.ReceiveTask -and -not $State.ReceiveTask.IsCompleted) {
                $State.Client.Abort()
                return
            }
            $timeout = New-TimeoutTokenSource -TimeoutMs $TimeoutMs
            try {
                [void]$State.Client.CloseAsync(
                    [Net.WebSockets.WebSocketCloseStatus]::NormalClosure,
                    'test complete',
                    $timeout.Token
                ).GetAwaiter().GetResult()
            }
            catch {
                $State.Client.Abort()
            }
            finally {
                $timeout.Dispose()
            }
        }
    }
    finally {
        if ($null -ne $State.ReceiveCts) {
            $State.ReceiveCts.Dispose()
        }
        $State.ReceiveStream.Dispose()
        $State.Client.Dispose()
    }
}

Export-ModuleMember -Function Connect-GraphQLWebSocket, Start-GraphQLWebSocketOperation, Stop-GraphQLWebSocketOperation, Receive-GraphQLWebSocketMessage, Close-GraphQLWebSocket
