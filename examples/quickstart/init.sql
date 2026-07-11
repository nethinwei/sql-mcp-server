CREATE TABLE app_user (
    id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email text NOT NULL,
    tenant_id integer NOT NULL
);

INSERT INTO app_user (email, tenant_id) VALUES
    ('alice@example.com', 7),
    ('bob@example.com', 8);
