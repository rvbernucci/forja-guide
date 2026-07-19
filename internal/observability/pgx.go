package observability

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type queryOperationKey struct{}

// PGXTracer instruments PostgreSQL queries without recording SQL or arguments.
type PGXTracer struct {
	observer *Observer
}

// NewPGXTracer creates a content-free pgx query tracer.
func NewPGXTracer(observer *Observer) *PGXTracer {
	if observer == nil {
		observer = NewObserver(nil, nil)
	}
	return &PGXTracer{observer: observer}
}

// TraceQueryStart starts one persistence span and deliberately ignores SQL.
func (tracer *PGXTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceQueryStartData,
) context.Context {
	ctx, operation := tracer.observer.Start(
		ctx,
		BoundaryPersistence,
		OperationQuery,
	)
	return context.WithValue(ctx, queryOperationKey{}, operation)
}

// TraceQueryEnd closes the span using only the stable error taxonomy.
func (tracer *PGXTracer) TraceQueryEnd(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryEndData,
) {
	operation, _ := ctx.Value(queryOperationKey{}).(*OperationHandle)
	operation.End(data.Err)
}
