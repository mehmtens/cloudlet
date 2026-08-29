package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Event struct {
	ActorUserID      *uuid.UUID
	Type, TargetType string
	TargetID         *uuid.UUID
	IP               string
	Metadata         map[string]any
}
type Recorder interface {
	Record(context.Context, Event) error
}
type PostgresRecorder struct{ db *pgxpool.Pool }

func NewPostgresRecorder(db *pgxpool.Pool) *PostgresRecorder { return &PostgresRecorder{db: db} }
func (r *PostgresRecorder) Record(ctx context.Context, event Event) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `INSERT INTO audit_events(id,actor_user_id,event_type,target_type,target_id,ip_address,metadata,created_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,NOW())`, uuid.New(), event.ActorUserID, event.Type, event.TargetType, event.TargetID, event.IP, string(metadata))
	return err
}
func Cutoff(retention time.Duration) time.Time { return time.Now().UTC().Add(-retention) }
