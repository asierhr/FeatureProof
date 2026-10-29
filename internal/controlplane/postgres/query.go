package postgres

import (
	"context"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
)

// ListJobs reads a stable project/state snapshot using an authenticated total-order keyset.
func (store *Store) ListJobs(ctx context.Context, query controlplane.JobQuery, codec *controlplane.CursorCodec) (controlplane.JobPage, error) {
	if store == nil || codec == nil || query.ProjectID == "" || query.Limit <= 0 || query.Limit > 1000 || query.Now.IsZero() || query.CursorTTL <= 0 {
		return controlplane.JobPage{}, controlplane.ErrInvalid
	}
	filter := string(query.State)
	if filter == "" {
		filter = "all"
	}
	if filter != "all" && query.State != controlplane.JobQueued && query.State != controlplane.JobLeased && query.State != controlplane.JobRetryWait && query.State != controlplane.JobSucceeded && query.State != controlplane.JobFailed && query.State != controlplane.JobDead && query.State != controlplane.JobCancelled {
		return controlplane.JobPage{}, controlplane.ErrInvalid
	}
	snapshot, lastAt, lastKey := query.Now.UTC(), time.Unix(0, 1).UTC(), "!"
	if query.Cursor != "" {
		cursor, err := codec.Open(query.Cursor, query.ProjectID, filter, query.Now)
		if err != nil {
			return controlplane.JobPage{}, err
		}
		snapshot, lastAt, lastKey = time.Unix(0, cursor.SnapshotUnixNano).UTC(), time.Unix(0, cursor.LastUnixNano).UTC(), cursor.LastKey
	}
	rows, err := store.pool.Query(ctx, `SELECT document,created_at,job_id FROM jobs WHERE project_id=$1 AND created_at<=$2 AND (created_at,job_id)>($3,$4) AND ($5='all' OR state=$5) ORDER BY created_at,job_id LIMIT $6`, query.ProjectID, snapshot, lastAt, lastKey, filter, query.Limit)
	if err != nil {
		return controlplane.JobPage{}, mapOperationError(err, "jobs.list")
	}
	defer rows.Close()
	page := controlplane.JobPage{Jobs: make([]controlplane.Job, 0, query.Limit)}
	var finalAt time.Time
	var finalKey string
	for rows.Next() {
		var document []byte
		if err = rows.Scan(&document, &finalAt, &finalKey); err != nil {
			return controlplane.JobPage{}, mapOperationError(err, "jobs.list")
		}
		job, decodeErr := decodeJob(document)
		if decodeErr != nil {
			return controlplane.JobPage{}, decodeErr
		}
		page.Jobs = append(page.Jobs, job)
	}
	if rows.Err() != nil {
		return controlplane.JobPage{}, mapOperationError(rows.Err(), "jobs.list")
	}
	if len(page.Jobs) == query.Limit {
		page.NextCursor, err = codec.Seal(controlplane.PageCursor{ProjectID: query.ProjectID, Filter: filter, SnapshotUnixNano: snapshot.UnixNano(), LastUnixNano: finalAt.UTC().UnixNano(), LastKey: finalKey, ExpiresAtUnixNano: snapshot.Add(query.CursorTTL).UnixNano()})
		if err != nil {
			return controlplane.JobPage{}, err
		}
	}
	store.observe(ctx, controlplane.Observation{Operation: "jobs.list", Count: int64(len(page.Jobs))})
	return page, nil
}
