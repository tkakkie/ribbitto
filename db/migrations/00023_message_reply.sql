-- +goose Up
ALTER TABLE message
  ADD COLUMN reply_to_message_id uuid,
  ADD COLUMN reply_to_member_id uuid,
  ADD CONSTRAINT message_reply_pair_check
    CHECK ((reply_to_message_id IS NULL) = (reply_to_member_id IS NULL)),
  ADD CONSTRAINT message_reply_self_check CHECK (reply_to_message_id <> id),
  ADD CONSTRAINT message_reply_message_fkey
    FOREIGN KEY (organization_id, channel_id, reply_to_message_id)
    REFERENCES message (organization_id, channel_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT message_reply_member_fkey
    FOREIGN KEY (organization_id, reply_to_member_id)
    REFERENCES member (organization_id, id) ON DELETE RESTRICT;

-- +goose StatementBegin
CREATE FUNCTION message_reply_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'message reply is immutable' USING ERRCODE = 'check_violation';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER message_reply_guard BEFORE UPDATE OF reply_to_message_id, reply_to_member_id ON message
  FOR EACH ROW WHEN (NEW.reply_to_message_id IS DISTINCT FROM OLD.reply_to_message_id
    OR NEW.reply_to_member_id IS DISTINCT FROM OLD.reply_to_member_id)
  EXECUTE FUNCTION message_reply_guard();

-- +goose Down
DROP TRIGGER message_reply_guard ON message;
DROP FUNCTION message_reply_guard();
ALTER TABLE message
  DROP COLUMN reply_to_message_id,
  DROP COLUMN reply_to_member_id;
