[CmdletBinding()]
param(
    [string]$BaseUrl = 'http://127.0.0.1:8081',
    [string]$WsUrl = 'ws://127.0.0.1:8081/query',
    [ValidateRange(1, 10)][int]$TimeoutSec = 5,
    [switch]$FailFast
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

Import-Module (Join-Path $PSScriptRoot 'lib/blackbox-common.psm1') -Force
Import-Module (Join-Path $PSScriptRoot 'lib/blackbox-websocket.psm1') -Force

$script:results = [Collections.Generic.List[object]]::new()
$script:run = [ordered]@{
    Prefix = 'blackbox-' + [Guid]::NewGuid().ToString('N').Substring(0, 12)
    Owner = 'owner-' + [Guid]::NewGuid().ToString('N')
    Other = 'other-' + [Guid]::NewGuid().ToString('N')
    Commenter = 'commenter-' + [Guid]::NewGuid().ToString('N')
}
$script:requestTimeoutSec = $TimeoutSec

function Invoke-GraphQLRequest {
    param(
        [Parameter(Mandatory)][string]$Query,
        [hashtable]$Variables = @{},
        [AllowNull()][string]$AuthorId
    )

    $headers = @{}
    if ($PSBoundParameters.ContainsKey('AuthorId')) {
        $headers['X-Author-ID'] = $AuthorId
    }
    $response = Invoke-WebRequest -Uri ($BaseUrl.TrimEnd('/') + '/query') -Method Post `
        -ContentType 'application/json' -Headers $headers `
        -Body (New-GraphQLBody -Query $Query -Variables $Variables) `
        -TimeoutSec $script:requestTimeoutSec -SkipHttpErrorCheck
    $body = $response.Content | ConvertFrom-Json -Depth 30
    return [pscustomobject]@{
        StatusCode = [int]$response.StatusCode
        Body = $body
        Raw = $response.Content
    }
}

function Assert-GraphQLSuccess {
    param([Parameter(Mandatory)]$Response)

    Assert-Equal -Expected 200 -Actual $Response.StatusCode -Message 'GraphQL HTTP status'
    $errorsProperty = $Response.Body.PSObject.Properties['errors']
    if ($null -ne $errorsProperty -and @($errorsProperty.Value).Count -gt 0) {
        throw "Unexpected GraphQL errors: $($errorsProperty.Value | ConvertTo-Json -Depth 20 -Compress)"
    }
    if ($null -eq $Response.Body.PSObject.Properties['data']) {
        throw "GraphQL response has no data: $($Response.Raw)"
    }
    return $Response.Body.data
}

function Assert-GraphQLError {
    param(
        [Parameter(Mandatory)]$Response,
        [Parameter(Mandatory)][string]$Code
    )

    Assert-Equal -Expected 200 -Actual $Response.StatusCode -Message 'GraphQL error HTTP status'
    $actual = Get-GraphQLErrorCode -Response $Response.Body
    Assert-Equal -Expected $Code -Actual $actual -Message 'GraphQL error code'
}

function Invoke-TestCase {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][scriptblock]$Body
    )

    $watch = [Diagnostics.Stopwatch]::StartNew()
    try {
        & $Body
        $watch.Stop()
        $script:results.Add([pscustomobject]@{ Name = $Name; Status = 'PASS'; DurationMs = $watch.ElapsedMilliseconds; Error = '' })
        Write-Host ("PASS {0} ({1} ms)" -f $Name, $watch.ElapsedMilliseconds) -ForegroundColor Green
    }
    catch {
        $watch.Stop()
        $message = $_.Exception.Message
        $script:results.Add([pscustomobject]@{ Name = $Name; Status = 'FAIL'; DurationMs = $watch.ElapsedMilliseconds; Error = $message })
        Write-Host ("FAIL {0} ({1} ms): {2}" -f $Name, $watch.ElapsedMilliseconds, $message) -ForegroundColor Red
        if ($FailFast) {
            throw
        }
    }
}

function New-TestPost {
    param(
        [Parameter(Mandatory)][string]$Title,
        [Parameter(Mandatory)][string]$Content,
        [Parameter(Mandatory)][string]$AuthorId
    )

    $response = Invoke-GraphQLRequest -Query @'
mutation($input: CreatePostInput!) {
  createPost(input: $input) { id authorID title content commentsEnabled createdAt }
}
'@ -Variables @{ input = @{ title = $Title; content = $Content } } -AuthorId $AuthorId
    return (Assert-GraphQLSuccess -Response $response).createPost
}

function New-TestComment {
    param(
        [Parameter(Mandatory)][string]$PostId,
        [AllowNull()]$ParentId,
        [Parameter(Mandatory)][string]$Text,
        [Parameter(Mandatory)][string]$AuthorId
    )

    $response = Invoke-GraphQLRequest -Query @'
mutation($input: CreateCommentInput!) {
  createComment(input: $input) { id postID parentID authorID text createdAt }
}
'@ -Variables @{ input = @{ postID = $PostId; parentID = $ParentId; text = $Text } } -AuthorId $AuthorId
    try {
        return (Assert-GraphQLSuccess -Response $response).createComment
    }
    catch {
        $parentLabel = if ($null -eq $ParentId) { '<null>' } else { $ParentId }
        throw "createComment failed (postID=$PostId; parentID=$parentLabel; textLength=$($Text.Length); authorID=$AuthorId): $($_.Exception.Message)"
    }
}

function Set-TestCommentsEnabled {
    param(
        [Parameter(Mandatory)][string]$PostId,
        [Parameter(Mandatory)][bool]$Enabled,
        [Parameter(Mandatory)][string]$AuthorId
    )

    $response = Invoke-GraphQLRequest -Query @'
mutation($postID: ID!, $enabled: Boolean!) {
  setPostCommentsEnabled(postID: $postID, enabled: $enabled) { id commentsEnabled }
}
'@ -Variables @{ postID = $PostId; enabled = $Enabled } -AuthorId $AuthorId
    return (Assert-GraphQLSuccess -Response $response).setPostCommentsEnabled
}

function Start-CommentSubscription {
    param(
        [Parameter(Mandatory)][hashtable]$Socket,
        [Parameter(Mandatory)][string]$OperationId,
        [Parameter(Mandatory)][string]$PostId
    )

    Start-GraphQLWebSocketOperation -State $Socket -OperationId $OperationId -Query @'
subscription($postID: ID!) {
  commentAdded(postID: $postID) { id postID parentID authorID text createdAt }
}
'@ -Variables @{ postID = $PostId }
    Start-Sleep -Milliseconds 150
}

function Receive-CommentEvent {
    param(
        [Parameter(Mandatory)][hashtable]$Socket,
        [Parameter(Mandatory)][string]$OperationId,
        [int]$TimeoutMs = 5000
    )

    $message = Receive-GraphQLWebSocketMessage -State $Socket -OperationId $OperationId -Types @('next', 'error', 'complete') -TimeoutMs $TimeoutMs
    if ($message.type -ne 'next') {
        throw "Subscription $OperationId returned $($message.type): $($message.payload | ConvertTo-Json -Depth 20 -Compress)"
    }
    $errorsProperty = $message.payload.PSObject.Properties['errors']
    if ($null -ne $errorsProperty -and @($errorsProperty.Value).Count -gt 0) {
        throw "Subscription $OperationId returned GraphQL errors: $($errorsProperty.Value | ConvertTo-Json -Depth 20 -Compress)"
    }
    return $message.payload.data.commentAdded
}

function Assert-NoCommentEvent {
    param(
        [Parameter(Mandatory)][hashtable]$Socket,
        [Parameter(Mandatory)][string]$OperationId,
        [int]$TimeoutMs = 600
    )

    $message = Receive-GraphQLWebSocketMessage -State $Socket -OperationId $OperationId -Types @('next', 'error') -TimeoutMs $TimeoutMs -AllowTimeout
    if ($null -ne $message) {
        throw "Unexpected subscription message for $OperationId`: $($message | ConvertTo-Json -Depth 20 -Compress)"
    }
}

function Assert-UUID {
    param([Parameter(Mandatory)][string]$Value, [Parameter(Mandatory)][string]$Message)

    $parsed = [Guid]::Empty
    Assert-True -Condition ([Guid]::TryParse($Value, [ref]$parsed)) -Message $Message
}

function ConvertTo-TestTimestamp {
    param([Parameter(Mandatory)]$Value)

    if ($Value -is [DateTimeOffset]) {
        return $Value
    }
    if ($Value -is [DateTime]) {
        return [DateTimeOffset]$Value
    }
    return [DateTimeOffset]::Parse(
        [string]$Value,
        [Globalization.CultureInfo]::InvariantCulture,
        [Globalization.DateTimeStyles]::RoundtripKind
    )
}

$fatal = $null
try {
    Invoke-TestCase 'health GET' {
        $response = Invoke-WebRequest -Uri ($BaseUrl.TrimEnd('/') + '/healthz') -TimeoutSec $script:requestTimeoutSec -SkipHttpErrorCheck
        Assert-Equal -Expected 200 -Actual ([int]$response.StatusCode) -Message 'Health HTTP status'
        Assert-Equal -Expected 'ok' -Actual $response.Content.Trim() -Message 'Health response body'
    }

    Invoke-TestCase 'mutation requires authentication' {
        $response = Invoke-GraphQLRequest -Query 'mutation { createPost(input: {title: "no auth", content: "body"}) { id } }'
        Assert-GraphQLError -Response $response -Code 'UNAUTHENTICATED'
    }

    Invoke-TestCase 'post creation and read' {
        $title = "$($script:run.Prefix)-primary"
        $post = New-TestPost -Title $title -Content 'primary content' -AuthorId "  $($script:run.Owner)  "
        Assert-UUID -Value $post.id -Message 'Created post ID is not a UUID'
        Assert-Equal -Expected $script:run.Owner -Actual $post.authorID -Message 'Author header was not trimmed'
        Assert-Equal -Expected $title -Actual $post.title -Message 'Created title'
        Assert-Equal -Expected $true -Actual $post.commentsEnabled -Message 'Comments default'
        [void](ConvertTo-TestTimestamp -Value $post.createdAt)
        $script:run.PrimaryPost = $post

        $response = Invoke-GraphQLRequest -Query 'query($id: ID!) { post(id: $id) { id authorID title content commentsEnabled createdAt } }' -Variables @{ id = $post.id }
        $read = (Assert-GraphQLSuccess -Response $response).post
        Assert-Equal -Expected $post.id -Actual $read.id -Message 'Read post ID'
        Assert-Equal -Expected 'primary content' -Actual $read.content -Message 'Read post content'
    }

    Invoke-TestCase 'only owner can change comment setting' {
        $response = Invoke-GraphQLRequest -Query 'mutation($id: ID!) { setPostCommentsEnabled(postID: $id, enabled: false) { id } }' `
            -Variables @{ id = $script:run.PrimaryPost.id } -AuthorId $script:run.Other
        Assert-GraphQLError -Response $response -Code 'FORBIDDEN'
        $read = Assert-GraphQLSuccess -Response (Invoke-GraphQLRequest -Query 'query($id: ID!) { post(id: $id) { commentsEnabled } }' -Variables @{ id = $script:run.PrimaryPost.id })
        Assert-Equal -Expected $true -Actual $read.post.commentsEnabled -Message 'Forbidden mutation changed state'
    }

    Invoke-TestCase 'root reply and nested reply form direct-child tree' {
        $postId = $script:run.PrimaryPost.id
        $root = New-TestComment -PostId $postId -ParentId $null -Text "$($script:run.Prefix)-root" -AuthorId $script:run.Commenter
        $reply = New-TestComment -PostId $postId -ParentId $root.id -Text "$($script:run.Prefix)-reply" -AuthorId $script:run.Other
        $nested = New-TestComment -PostId $postId -ParentId $reply.id -Text "$($script:run.Prefix)-nested" -AuthorId $script:run.Commenter
        $script:run.Root = $root
        $script:run.Reply = $reply
        $script:run.Nested = $nested

        $response = Invoke-GraphQLRequest -Query @'
query($id: ID!) {
  post(id: $id) {
    comments(first: 3) {
      nodes { id postID parentID text replies(first: 3) { nodes { id parentID text replies(first: 3) { nodes { id parentID text } } } } }
    }
  }
}
'@ -Variables @{ id = $postId }
        $roots = @((Assert-GraphQLSuccess -Response $response).post.comments.nodes)
        $foundRoot = @($roots | Where-Object id -eq $root.id)
        Assert-Equal -Expected 1 -Actual $foundRoot.Count -Message 'Root was not returned exactly once'
        Assert-Equal -Expected $null -Actual $foundRoot[0].parentID -Message 'Root parentID'
        $directReplies = @($foundRoot[0].replies.nodes)
        Assert-Equal -Expected 1 -Actual $directReplies.Count -Message 'Root direct reply count'
        Assert-Equal -Expected $reply.id -Actual $directReplies[0].id -Message 'Root direct reply ID'
        $nestedReplies = @($directReplies[0].replies.nodes)
        Assert-Equal -Expected 1 -Actual $nestedReplies.Count -Message 'Nested reply count'
        Assert-Equal -Expected $nested.id -Actual $nestedReplies[0].id -Message 'Nested reply ID'
    }

    Invoke-TestCase 'disabled comments block roots and replies' {
        $postId = $script:run.PrimaryPost.id
        $disabled = Set-TestCommentsEnabled -PostId $postId -Enabled $false -AuthorId $script:run.Owner
        Assert-Equal -Expected $false -Actual $disabled.commentsEnabled -Message 'Comments were not disabled'

        $rootResponse = Invoke-GraphQLRequest -Query 'mutation($input: CreateCommentInput!) { createComment(input: $input) { id } }' `
            -Variables @{ input = @{ postID = $postId; parentID = $null; text = 'blocked root' } } -AuthorId $script:run.Commenter
        Assert-GraphQLError -Response $rootResponse -Code 'COMMENTS_DISABLED'
        $replyResponse = Invoke-GraphQLRequest -Query 'mutation($input: CreateCommentInput!) { createComment(input: $input) { id } }' `
            -Variables @{ input = @{ postID = $postId; parentID = $script:run.Root.id; text = 'blocked reply' } } -AuthorId $script:run.Commenter
        Assert-GraphQLError -Response $replyResponse -Code 'COMMENTS_DISABLED'
    }

    Invoke-TestCase 'owner can re-enable comments' {
        $postId = $script:run.PrimaryPost.id
        $enabled = Set-TestCommentsEnabled -PostId $postId -Enabled $true -AuthorId $script:run.Owner
        Assert-Equal -Expected $true -Actual $enabled.commentsEnabled -Message 'Comments were not enabled'
        $comment = New-TestComment -PostId $postId -ParentId $null -Text "$($script:run.Prefix)-after-enable" -AuthorId $script:run.Commenter
        Assert-UUID -Value $comment.id -Message 'Comment after re-enable has invalid ID'
    }

    Invoke-TestCase 'missing and invalid post IDs' {
        $missing = [Guid]::NewGuid().ToString()
        $read = Invoke-GraphQLRequest -Query 'query($id: ID!) { post(id: $id) { id } }' -Variables @{ id = $missing }
        $data = Assert-GraphQLSuccess -Response $read
        Assert-Equal -Expected $null -Actual $data.post -Message 'Missing post should be null'

        $invalid = Invoke-GraphQLRequest -Query 'query { post(id: "not-a-uuid") { id } }'
        Assert-GraphQLError -Response $invalid -Code 'VALIDATION_FAILED'

        $create = Invoke-GraphQLRequest -Query 'mutation($id: ID!) { createComment(input: {postID: $id, text: "missing"}) { id } }' `
            -Variables @{ id = $missing } -AuthorId $script:run.Commenter
        Assert-GraphQLError -Response $create -Code 'NOT_FOUND'
    }

    Invoke-TestCase 'missing and foreign parents are rejected' {
        $missingParent = Invoke-GraphQLRequest -Query 'mutation($input: CreateCommentInput!) { createComment(input: $input) { id } }' `
            -Variables @{ input = @{ postID = $script:run.PrimaryPost.id; parentID = [Guid]::NewGuid().ToString(); text = 'missing parent' } } `
            -AuthorId $script:run.Commenter
        Assert-GraphQLError -Response $missingParent -Code 'NOT_FOUND'

        $otherPost = New-TestPost -Title "$($script:run.Prefix)-foreign" -Content 'foreign parent post' -AuthorId $script:run.Owner
        $foreign = Invoke-GraphQLRequest -Query 'mutation($input: CreateCommentInput!) { createComment(input: $input) { id } }' `
            -Variables @{ input = @{ postID = $otherPost.id; parentID = $script:run.Root.id; text = 'foreign parent' } } `
            -AuthorId $script:run.Commenter
        Assert-GraphQLError -Response $foreign -Code 'VALIDATION_FAILED'
    }

    Invoke-TestCase 'blank inputs are rejected' {
        $blankPost = Invoke-GraphQLRequest -Query 'mutation($input: CreatePostInput!) { createPost(input: $input) { id } }' `
            -Variables @{ input = @{ title = '   '; content = 'body' } } -AuthorId $script:run.Owner
        Assert-GraphQLError -Response $blankPost -Code 'VALIDATION_FAILED'
        $blankComment = Invoke-GraphQLRequest -Query 'mutation($input: CreateCommentInput!) { createComment(input: $input) { id } }' `
            -Variables @{ input = @{ postID = $script:run.PrimaryPost.id; parentID = $null; text = "`t " } } -AuthorId $script:run.Commenter
        Assert-GraphQLError -Response $blankComment -Code 'VALIDATION_FAILED'
    }

    Invoke-TestCase 'over-limit inputs are rejected' {
        $title = Invoke-GraphQLRequest -Query 'mutation($input: CreatePostInput!) { createPost(input: $input) { id } }' `
            -Variables @{ input = @{ title = ('x' * 201); content = 'body' } } -AuthorId $script:run.Owner
        Assert-GraphQLError -Response $title -Code 'VALIDATION_FAILED'
        $content = Invoke-GraphQLRequest -Query 'mutation($input: CreatePostInput!) { createPost(input: $input) { id } }' `
            -Variables @{ input = @{ title = 'title'; content = ('x' * 20001) } } -AuthorId $script:run.Owner
        Assert-GraphQLError -Response $content -Code 'VALIDATION_FAILED'
        $comment = Invoke-GraphQLRequest -Query 'mutation($input: CreateCommentInput!) { createComment(input: $input) { id } }' `
            -Variables @{ input = @{ postID = $script:run.PrimaryPost.id; parentID = $null; text = ('x' * 2001) } } -AuthorId $script:run.Commenter
        Assert-GraphQLError -Response $comment -Code 'VALIDATION_FAILED'
    }

    Invoke-TestCase 'valid input boundaries are accepted' {
        $post = New-TestPost -Title ('Ж' * 200) -Content ('я' * 20000) -AuthorId $script:run.Owner
        Assert-UUID -Value $post.id -Message 'Boundary post ID'
        $comment = New-TestComment -PostId $post.id -ParentId $null -Text ('界' * 2000) -AuthorId $script:run.Commenter
        Assert-UUID -Value $comment.id -Message 'Boundary comment ID'
    }

    Invoke-TestCase 'post cursor pagination is disjoint' {
        $wanted = [Collections.Generic.List[string]]::new()
        foreach ($index in 1..5) {
            $wanted.Add((New-TestPost -Title "$($script:run.Prefix)-page-post-$index" -Content 'page' -AuthorId $script:run.Owner).id)
        }
        $seen = [Collections.Generic.HashSet[string]]::new()
        $after = $null
        $pages = 0
        $previousTime = [DateTimeOffset]::MaxValue
        while ($pages -lt 100) {
            $response = Invoke-GraphQLRequest -Query 'query($after: String) { posts(first: 2, after: $after) { nodes { id createdAt } pageInfo { hasNextPage endCursor } } }' -Variables @{ after = $after }
            $connection = (Assert-GraphQLSuccess -Response $response).posts
            $nodes = @($connection.nodes)
            foreach ($node in $nodes) {
                Assert-True -Condition ($seen.Add([string]$node.id)) -Message "Duplicate post across pages: $($node.id)"
                $currentTime = ConvertTo-TestTimestamp -Value $node.createdAt
                Assert-True -Condition ($currentTime -le $previousTime) -Message 'Post pagination is not newest-first'
                $previousTime = $currentTime
            }
            if ($nodes.Count -gt 0) {
                Assert-True -Condition (-not [string]::IsNullOrWhiteSpace([string]$connection.pageInfo.endCursor)) -Message 'Nonempty post page has no cursor'
            }
            $pages++
            $allFound = @($wanted | Where-Object { -not $seen.Contains($_) }).Count -eq 0
            if ($allFound -and $pages -ge 2) { break }
            if (-not $connection.pageInfo.hasNextPage) { break }
            $after = [string]$connection.pageInfo.endCursor
        }
        Assert-True -Condition ($pages -ge 2) -Message 'Post pagination did not cross a page boundary'
        foreach ($id in $wanted) {
            Assert-True -Condition $seen.Contains($id) -Message "Created post missing from pagination: $id"
        }
    }

    Invoke-TestCase 'root comment cursor pagination is disjoint and complete' {
        $post = New-TestPost -Title "$($script:run.Prefix)-root-pages" -Content 'root pages' -AuthorId $script:run.Owner
        $wanted = [Collections.Generic.HashSet[string]]::new()
        foreach ($index in 1..5) {
            [void]$wanted.Add((New-TestComment -PostId $post.id -ParentId $null -Text "root-$index" -AuthorId $script:run.Commenter).id)
        }
        $seen = [Collections.Generic.HashSet[string]]::new()
        $after = $null
        $pages = 0
        do {
            $response = Invoke-GraphQLRequest -Query 'query($id: ID!, $after: String) { post(id: $id) { comments(first: 2, after: $after) { nodes { id parentID } pageInfo { hasNextPage endCursor } } } }' `
                -Variables @{ id = $post.id; after = $after }
            $connection = (Assert-GraphQLSuccess -Response $response).post.comments
            foreach ($node in @($connection.nodes)) {
                Assert-Equal -Expected $null -Actual $node.parentID -Message 'Root page returned a reply'
                Assert-True -Condition ($seen.Add([string]$node.id)) -Message "Duplicate root comment: $($node.id)"
            }
            $pages++
            $after = $connection.pageInfo.endCursor
        } while ($connection.pageInfo.hasNextPage -and $pages -lt 10)
        Assert-Equal -Expected 3 -Actual $pages -Message 'Root comment page count'
        Assert-Equal -Expected 5 -Actual $seen.Count -Message 'Root comment total'
        foreach ($id in $wanted) { Assert-True -Condition $seen.Contains($id) -Message "Missing root comment $id" }
    }

    Invoke-TestCase 'reply cursor pagination is disjoint and complete' {
        $post = New-TestPost -Title "$($script:run.Prefix)-reply-pages" -Content 'reply pages' -AuthorId $script:run.Owner
        $parent = New-TestComment -PostId $post.id -ParentId $null -Text 'parent' -AuthorId $script:run.Commenter
        $wanted = [Collections.Generic.HashSet[string]]::new()
        foreach ($index in 1..5) {
            [void]$wanted.Add((New-TestComment -PostId $post.id -ParentId $parent.id -Text "reply-$index" -AuthorId $script:run.Commenter).id)
        }
        $seen = [Collections.Generic.HashSet[string]]::new()
        $after = $null
        $pages = 0
        do {
            $response = Invoke-GraphQLRequest -Query 'query($id: ID!, $after: String) { post(id: $id) { comments(first: 1) { nodes { id replies(first: 2, after: $after) { nodes { id parentID } pageInfo { hasNextPage endCursor } } } } } }' `
                -Variables @{ id = $post.id; after = $after }
            $rootNodes = @((Assert-GraphQLSuccess -Response $response).post.comments.nodes)
            Assert-Equal -Expected 1 -Actual $rootNodes.Count -Message 'Reply pagination root count'
            Assert-Equal -Expected $parent.id -Actual $rootNodes[0].id -Message 'Reply pagination parent'
            $connection = $rootNodes[0].replies
            foreach ($node in @($connection.nodes)) {
                Assert-Equal -Expected $parent.id -Actual $node.parentID -Message 'Reply parent ID'
                Assert-True -Condition ($seen.Add([string]$node.id)) -Message "Duplicate reply: $($node.id)"
            }
            $pages++
            $after = $connection.pageInfo.endCursor
        } while ($connection.pageInfo.hasNextPage -and $pages -lt 10)
        Assert-Equal -Expected 3 -Actual $pages -Message 'Reply page count'
        Assert-Equal -Expected 5 -Actual $seen.Count -Message 'Reply total'
        foreach ($id in $wanted) { Assert-True -Condition $seen.Contains($id) -Message "Missing reply $id" }
    }

    Invoke-TestCase 'normal nested query stays within complexity limit' {
        $response = Invoke-GraphQLRequest -Query 'query($id: ID!) { post(id: $id) { title comments(first: 2) { nodes { id text replies(first: 2) { nodes { id text } } } } } }' `
            -Variables @{ id = $script:run.PrimaryPost.id }
        [void](Assert-GraphQLSuccess -Response $response)
    }

    Invoke-TestCase 'extreme nested query is rejected by complexity limit' {
        $response = Invoke-GraphQLRequest -Query '{ posts(first: 100) { nodes { comments(first: 100) { nodes { replies(first: 100) { nodes { id } } } } } } }'
        Assert-GraphQLError -Response $response -Code 'COMPLEXITY_LIMIT_EXCEEDED'
    }

    Invoke-TestCase 'WebSocket graphql-transport-ws handshake' {
        $socket = $null
        try {
            $socket = Connect-GraphQLWebSocket -Uri $WsUrl -TimeoutMs ($TimeoutSec * 1000)
            Assert-Equal -Expected 'graphql-transport-ws' -Actual $socket.Client.SubProtocol -Message 'Negotiated WebSocket protocol'
        }
        finally {
            Close-GraphQLWebSocket -State $socket
        }
    }

    Invoke-TestCase 'subscription publishes roots and replies' {
        $post = New-TestPost -Title "$($script:run.Prefix)-subscription-tree" -Content 'subscription tree' -AuthorId $script:run.Owner
        $socket = $null
        try {
            $socket = Connect-GraphQLWebSocket -Uri $WsUrl -TimeoutMs ($TimeoutSec * 1000)
            Start-CommentSubscription -Socket $socket -OperationId 'tree' -PostId $post.id
            $root = New-TestComment -PostId $post.id -ParentId $null -Text 'subscription root' -AuthorId $script:run.Commenter
            $rootEvent = Receive-CommentEvent -Socket $socket -OperationId 'tree' -TimeoutMs ($TimeoutSec * 1000)
            Assert-Equal -Expected $root.id -Actual $rootEvent.id -Message 'Root event ID'
            Assert-Equal -Expected $null -Actual $rootEvent.parentID -Message 'Root event parent'
            $reply = New-TestComment -PostId $post.id -ParentId $root.id -Text 'subscription reply' -AuthorId $script:run.Other
            $replyEvent = Receive-CommentEvent -Socket $socket -OperationId 'tree' -TimeoutMs ($TimeoutSec * 1000)
            Assert-Equal -Expected $reply.id -Actual $replyEvent.id -Message 'Reply event ID'
            Assert-Equal -Expected $root.id -Actual $replyEvent.parentID -Message 'Reply event parent'
        }
        finally {
            Close-GraphQLWebSocket -State $socket
        }
    }

    Invoke-TestCase 'subscriptions filter events by post' {
        $postA = New-TestPost -Title "$($script:run.Prefix)-filter-a" -Content 'A' -AuthorId $script:run.Owner
        $postB = New-TestPost -Title "$($script:run.Prefix)-filter-b" -Content 'B' -AuthorId $script:run.Owner
        $socket = $null
        try {
            $socket = Connect-GraphQLWebSocket -Uri $WsUrl -TimeoutMs ($TimeoutSec * 1000)
            Start-CommentSubscription -Socket $socket -OperationId 'post-a' -PostId $postA.id
            Start-CommentSubscription -Socket $socket -OperationId 'post-b' -PostId $postB.id
            $commentA = New-TestComment -PostId $postA.id -ParentId $null -Text 'only A' -AuthorId $script:run.Commenter
            $eventA = Receive-CommentEvent -Socket $socket -OperationId 'post-a' -TimeoutMs ($TimeoutSec * 1000)
            Assert-Equal -Expected $commentA.id -Actual $eventA.id -Message 'Post A event'
            Assert-NoCommentEvent -Socket $socket -OperationId 'post-b'

            $commentB = New-TestComment -PostId $postB.id -ParentId $null -Text 'only B' -AuthorId $script:run.Commenter
            $eventB = Receive-CommentEvent -Socket $socket -OperationId 'post-b' -TimeoutMs ($TimeoutSec * 1000)
            Assert-Equal -Expected $commentB.id -Actual $eventB.id -Message 'Post B event after negative window'
        }
        finally {
            Close-GraphQLWebSocket -State $socket
        }
    }

    Invoke-TestCase 'failed comment mutation emits no event' {
        $post = New-TestPost -Title "$($script:run.Prefix)-failed-event" -Content 'failed event' -AuthorId $script:run.Owner
        [void](Set-TestCommentsEnabled -PostId $post.id -Enabled $false -AuthorId $script:run.Owner)
        $socket = $null
        try {
            $socket = Connect-GraphQLWebSocket -Uri $WsUrl -TimeoutMs ($TimeoutSec * 1000)
            Start-CommentSubscription -Socket $socket -OperationId 'failed' -PostId $post.id
            $failed = Invoke-GraphQLRequest -Query 'mutation($id: ID!) { createComment(input: {postID: $id, text: "blocked"}) { id } }' `
                -Variables @{ id = $post.id } -AuthorId $script:run.Commenter
            Assert-GraphQLError -Response $failed -Code 'COMMENTS_DISABLED'
            Assert-NoCommentEvent -Socket $socket -OperationId 'failed'

            [void](Set-TestCommentsEnabled -PostId $post.id -Enabled $true -AuthorId $script:run.Owner)
            $saved = New-TestComment -PostId $post.id -ParentId $null -Text 'saved later' -AuthorId $script:run.Commenter
            $event = Receive-CommentEvent -Socket $socket -OperationId 'failed' -TimeoutMs ($TimeoutSec * 1000)
            Assert-Equal -Expected $saved.id -Actual $event.id -Message 'Positive event after failed mutation'
        }
        finally {
            Close-GraphQLWebSocket -State $socket
        }
    }

    Invoke-TestCase 'two subscribers receive the same saved comment' {
        $post = New-TestPost -Title "$($script:run.Prefix)-fanout" -Content 'fanout' -AuthorId $script:run.Owner
        $first = $null
        $second = $null
        try {
            $first = Connect-GraphQLWebSocket -Uri $WsUrl -TimeoutMs ($TimeoutSec * 1000)
            $second = Connect-GraphQLWebSocket -Uri $WsUrl -TimeoutMs ($TimeoutSec * 1000)
            Start-CommentSubscription -Socket $first -OperationId 'fanout-1' -PostId $post.id
            Start-CommentSubscription -Socket $second -OperationId 'fanout-2' -PostId $post.id
            $saved = New-TestComment -PostId $post.id -ParentId $null -Text 'fanout event' -AuthorId $script:run.Commenter
            $event1 = Receive-CommentEvent -Socket $first -OperationId 'fanout-1' -TimeoutMs ($TimeoutSec * 1000)
            $event2 = Receive-CommentEvent -Socket $second -OperationId 'fanout-2' -TimeoutMs ($TimeoutSec * 1000)
            Assert-Equal -Expected $saved.id -Actual $event1.id -Message 'First subscriber event'
            Assert-Equal -Expected $saved.id -Actual $event2.id -Message 'Second subscriber event'
        }
        finally {
            Close-GraphQLWebSocket -State $first
            Close-GraphQLWebSocket -State $second
        }
    }

    Invoke-TestCase 'client complete stops subscription delivery' {
        $post = New-TestPost -Title "$($script:run.Prefix)-complete" -Content 'complete' -AuthorId $script:run.Owner
        $socket = $null
        try {
            $socket = Connect-GraphQLWebSocket -Uri $WsUrl -TimeoutMs ($TimeoutSec * 1000)
            Start-CommentSubscription -Socket $socket -OperationId 'complete-test' -PostId $post.id
            $armed = New-TestComment -PostId $post.id -ParentId $null -Text 'arming event' -AuthorId $script:run.Commenter
            $armedEvent = Receive-CommentEvent -Socket $socket -OperationId 'complete-test' -TimeoutMs ($TimeoutSec * 1000)
            Assert-Equal -Expected $armed.id -Actual $armedEvent.id -Message 'Arming event'
            Stop-GraphQLWebSocketOperation -State $socket -OperationId 'complete-test'
            Start-Sleep -Milliseconds 150
            [void](New-TestComment -PostId $post.id -ParentId $null -Text 'after complete' -AuthorId $script:run.Commenter)
            Assert-NoCommentEvent -Socket $socket -OperationId 'complete-test'
        }
        finally {
            Close-GraphQLWebSocket -State $socket
        }
    }
}
catch {
    $fatal = $_
}
finally {
    Write-Host ''
    $script:results | Format-Table Status, DurationMs, Name -AutoSize
    $passed = @($script:results | Where-Object Status -eq 'PASS').Count
    $failed = @($script:results | Where-Object Status -eq 'FAIL').Count
    $duration = ($script:results | Measure-Object -Property DurationMs -Sum).Sum
    Write-Host ("Total: {0}; Passed: {1}; Failed: {2}; Duration: {3} ms; Run: {4}" -f $script:results.Count, $passed, $failed, $duration, $script:run.Prefix)
    if ($failed -gt 0) {
        Write-Host 'Failures:' -ForegroundColor Red
        $script:results | Where-Object Status -eq 'FAIL' | ForEach-Object { Write-Host ("- {0}: {1}" -f $_.Name, $_.Error) -ForegroundColor Red }
    }
}

if ($null -ne $fatal -or @($script:results | Where-Object Status -eq 'FAIL').Count -gt 0) {
    exit 1
}
exit 0
