param(
    [string]$BaseUrl = "http://127.0.0.1:8081",
    [string]$AuthorId = "smoke-test-author"
)

$ErrorActionPreference = "Stop"
$headers = @{ "X-Author-ID" = $AuthorId }

function Invoke-GraphQL {
    param(
        [Parameter(Mandatory = $true)][string]$Query,
        [hashtable]$Variables = @{}
    )

    $body = @{ query = $Query; variables = $Variables } | ConvertTo-Json -Depth 10 -Compress
    $response = Invoke-RestMethod -Uri "$BaseUrl/query" -Method Post -ContentType "application/json" -Headers $headers -Body $body
    if ($response.errors) {
        throw "GraphQL returned errors: $($response.errors | ConvertTo-Json -Depth 10 -Compress)"
    }
    return $response.data
}

$health = Invoke-WebRequest -Uri "$BaseUrl/healthz" -UseBasicParsing -TimeoutSec 5
if ($health.StatusCode -ne 200) {
    throw "Health check returned HTTP $($health.StatusCode)"
}

$post = Invoke-GraphQL -Query 'mutation { createPost(input: {title: "Smoke test", content: "PostgreSQL verification"}) { id authorID } }'
$postId = $post.createPost.id
if (-not $postId -or $post.createPost.authorID -ne $AuthorId) {
    throw "Post creation result is invalid"
}

$comment = Invoke-GraphQL -Query 'mutation($postID: ID!) { createComment(input: {postID: $postID, text: "Root comment"}) { id } }' -Variables @{ postID = $postId }
$commentId = $comment.createComment.id
if (-not $commentId) {
    throw "Root comment was not created"
}

$reply = Invoke-GraphQL -Query 'mutation($postID: ID!, $parentID: ID!) { createComment(input: {postID: $postID, parentID: $parentID, text: "Reply"}) { id } }' -Variables @{ postID = $postId; parentID = $commentId }
$replyId = $reply.createComment.id
if (-not $replyId) {
    throw "Reply was not created"
}

$result = Invoke-GraphQL -Query 'query($postID: ID!) { post(id: $postID) { title comments(first: 10) { nodes { id text authorID replies(first: 10) { nodes { id text } } } } } }' -Variables @{ postID = $postId }
$nodes = @($result.post.comments.nodes)
if ($result.post.title -ne "Smoke test" -or $nodes.Count -ne 1) {
    throw "Unexpected post or root comments response"
}
$replies = @($nodes[0].replies.nodes)
if ($nodes[0].id -ne $commentId -or $replies.Count -ne 1 -or $replies[0].id -ne $replyId) {
    throw "Unexpected nested replies response"
}

[pscustomobject]@{
    status = "ok"
    storage = "postgres"
    baseUrl = $BaseUrl
    postId = $postId
    commentId = $commentId
    replyId = $replyId
} | ConvertTo-Json
