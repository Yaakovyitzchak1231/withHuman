-- Notification windows throttle approval prompts per person, organization
-- and contact kind (design doc 17). A row is an open window: the first
-- prompt in a quiet period went out at once, and prompts due before
-- closes_at are held until it. Both tables are working state; history is
-- the notification.digested audit event.
CREATE TABLE notification_windows (
    organization_id uuid NOT NULL REFERENCES organizations(id),
    user_id uuid NOT NULL REFERENCES users(id),
    kind text NOT NULL,
    level integer NOT NULL CHECK (level >= 0),
    opened_at timestamp with time zone NOT NULL,
    closes_at timestamp with time zone NOT NULL CHECK (closes_at > opened_at),
    PRIMARY KEY (organization_id, user_id, kind)
);
CREATE INDEX notification_windows_user_idx ON notification_windows (user_id);

-- A hold is one request waiting in its key's current window. Closing the
-- window deletes every hold it had.
CREATE TABLE notification_holds (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    request_id uuid NOT NULL,
    held_at timestamp with time zone NOT NULL,
    PRIMARY KEY (organization_id, user_id, kind, request_id),
    FOREIGN KEY (organization_id, user_id, kind) REFERENCES notification_windows(organization_id, user_id, kind) ON DELETE CASCADE,
    FOREIGN KEY (request_id, organization_id) REFERENCES approval_requests(id, organization_id)
);
CREATE INDEX notification_holds_request_idx ON notification_holds (request_id, organization_id);
