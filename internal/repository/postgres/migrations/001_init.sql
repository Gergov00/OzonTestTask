CREATE TABLE IF NOT EXISTS posts (
    id uuid PRIMARY KEY,
    author_id text NOT NULL,
    title text NOT NULL,
    content text NOT NULL,
    comments_enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS comments (
    id uuid PRIMARY KEY,
    post_id uuid NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    parent_id uuid NULL REFERENCES comments(id) ON DELETE CASCADE,
    author_id text NOT NULL,
    text text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS posts_page_idx
    ON posts (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS comments_parent_page_idx
    ON comments (post_id, parent_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS comments_root_page_idx
    ON comments (post_id, created_at DESC, id DESC) WHERE parent_id IS NULL;
CREATE INDEX IF NOT EXISTS comments_parent_id_idx
    ON comments (parent_id);
