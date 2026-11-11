-- name: GetGitPushByApproval :one
SELECT * FROM git_pushes WHERE approval_id = $1;

-- name: GetGitPush :one
SELECT * FROM git_pushes WHERE id = $1;

-- name: LockGitPushByApproval :one
SELECT * FROM git_pushes WHERE approval_id = $1 FOR UPDATE;

-- name: InsertGitPush :one
INSERT INTO git_pushes (id, approval_id, task_id, workflow_run_id, repository_id, account_id, workspace_id, remote, branch, sha, base_branch)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: RetryGitPush :one
UPDATE git_pushes SET status = 'granted', attempts = attempts + 1, error = NULL, updated_at = now()
WHERE id = $1 AND status = 'failed'
RETURNING *;

-- name: FinishGitPush :one
UPDATE git_pushes SET status = $2, existed = $3, error = $4, updated_at = now(),
    pushed_at = CASE WHEN $2 = 'pushed' THEN now() ELSE pushed_at END
WHERE id = $1 AND status = 'granted'
RETURNING *;

-- name: ListGitPushesForTask :many
SELECT * FROM git_pushes WHERE task_id = $1 ORDER BY created_at DESC;

-- name: GetPullRequestByPush :one
SELECT * FROM git_pull_requests WHERE push_id = $1;

-- name: InsertPullRequest :one
INSERT INTO git_pull_requests (id, push_id, approval_id, draft, title) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: RetryPullRequest :one
UPDATE git_pull_requests SET status = 'granted', error = NULL, updated_at = now() WHERE id = $1 AND status = 'failed' RETURNING *;

-- name: FinishPullRequest :one
UPDATE git_pull_requests SET status = $2, number = $3, url = $4, state = $5, error = $6, updated_at = now(), refreshed_at = now()
WHERE id = $1 AND status = 'granted'
RETURNING *;

-- name: SetPullRequestState :exec
UPDATE git_pull_requests SET state = $2, draft = $3, refreshed_at = now(), updated_at = now() WHERE id = $1;

-- name: PullRequestsToRefresh :many
SELECT pr.*, p.remote, p.account_id FROM git_pull_requests pr JOIN git_pushes p ON p.id = pr.push_id
WHERE pr.status = 'opened' AND coalesce(pr.state, 'open') = 'open'
  AND (pr.refreshed_at IS NULL OR pr.refreshed_at < now() - make_interval(mins => sqlc.arg(minutes)::int))
LIMIT 50;

-- name: ListPullRequestsForTask :many
SELECT pr.* FROM git_pull_requests pr JOIN git_pushes p ON p.id = pr.push_id WHERE p.task_id = $1 ORDER BY pr.created_at DESC;
