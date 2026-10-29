ALTER TABLE packages ADD COLUMN backend_epoch bigint;
ALTER TABLE packages ADD COLUMN backend_identity text;
UPDATE packages SET backend_epoch=1, backend_identity='legacy';
ALTER TABLE packages ALTER COLUMN backend_epoch SET NOT NULL;
ALTER TABLE packages ALTER COLUMN backend_identity SET NOT NULL;
