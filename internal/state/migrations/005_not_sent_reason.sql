-- Why an effect was definitely not sent: the transport's error, kept as
-- an artifact. A turn that ended as soon as it started carries what its pane
-- showed, such as a model the CLI rejects. Each failed attempt replaces the
-- last, so an effect that failed for good holds the reason it stopped. A run
-- paused as dispatch_failed reads it back to say why.
ALTER TABLE effects ADD COLUMN not_sent_hash TEXT REFERENCES artifacts(hash);
