CREATE TABLE jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    file_name TEXT NOT NULL,
    file_key TEXT,
    result_key TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,

    CONSTRAINT jobs_status_check
        CHECK (
            status IN (
                'PENDING',
                'PROCESSING',
                'COMPLETED',
                'FAILED'
            )
        )
);

CREATE INDEX idx_jobs_status_created_at
    ON jobs (status, created_at DESC);

CREATE INDEX idx_jobs_created_at
    ON jobs (created_at DESC);