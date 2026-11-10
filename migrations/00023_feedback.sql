-- +goose Up
-- Your ratings of agent results (a run or an artifact): one per user and
-- target, attributed to the agent and definition version that produced it.
CREATE TABLE feedback (
    id                  uuid PRIMARY KEY,
    target_type         text NOT NULL CHECK (target_type IN ('run', 'artifact')),
    target_id           uuid NOT NULL,
    run_id              uuid REFERENCES agent_runs (id) ON DELETE CASCADE,
    artifact_id         uuid REFERENCES artifacts (id) ON DELETE CASCADE,
    agent_name          text,
    agent_definition_id uuid REFERENCES agent_definitions (id) ON DELETE SET NULL,
    task_id             uuid REFERENCES tasks (id) ON DELETE CASCADE,
    rating              text NOT NULL CHECK (rating IN ('up', 'down')),
    note                text NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    user_id             uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, target_type, target_id)
);
CREATE INDEX feedback_agent_idx ON feedback (agent_name, created_at DESC);

-- +goose Down
DROP TABLE feedback;
