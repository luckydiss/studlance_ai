package httpapi

import (
	"context"
	"errors"
)

// errNotImplemented marks worker endpoints that arrive in PR 3.
var errNotImplemented = errors.New("worker API is not implemented yet (PR 3)")

// WorkerRegister implements POST /api/worker/register.
func (s *Server) WorkerRegister(ctx context.Context, request WorkerRegisterRequestObject) (WorkerRegisterResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerClaim implements POST /api/worker/claim.
func (s *Server) WorkerClaim(ctx context.Context, request WorkerClaimRequestObject) (WorkerClaimResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerHeartbeat implements POST /api/worker/jobs/{id}/heartbeat.
func (s *Server) WorkerHeartbeat(ctx context.Context, request WorkerHeartbeatRequestObject) (WorkerHeartbeatResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerListInput implements GET /api/worker/jobs/{id}/input.
func (s *Server) WorkerListInput(ctx context.Context, request WorkerListInputRequestObject) (WorkerListInputResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerGetInput implements GET /api/worker/jobs/{id}/input/{path}.
func (s *Server) WorkerGetInput(ctx context.Context, request WorkerGetInputRequestObject) (WorkerGetInputResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerCreateRun implements POST /api/worker/jobs/{id}/runs.
func (s *Server) WorkerCreateRun(ctx context.Context, request WorkerCreateRunRequestObject) (WorkerCreateRunResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerPatchRun implements PATCH /api/worker/jobs/{id}/runs/{run_id}.
func (s *Server) WorkerPatchRun(ctx context.Context, request WorkerPatchRunRequestObject) (WorkerPatchRunResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerAppendSteps implements POST /api/worker/jobs/{id}/runs/{run_id}/steps.
func (s *Server) WorkerAppendSteps(ctx context.Context, request WorkerAppendStepsRequestObject) (WorkerAppendStepsResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerPutRunLog implements PUT /api/worker/jobs/{id}/runs/{run_id}/log.
func (s *Server) WorkerPutRunLog(ctx context.Context, request WorkerPutRunLogRequestObject) (WorkerPutRunLogResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerSetState implements POST /api/worker/jobs/{id}/state.
func (s *Server) WorkerSetState(ctx context.Context, request WorkerSetStateRequestObject) (WorkerSetStateResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerPutSnapshotFile implements PUT /api/worker/jobs/{id}/snapshot/{snapshot}/files.
func (s *Server) WorkerPutSnapshotFile(ctx context.Context, request WorkerPutSnapshotFileRequestObject) (WorkerPutSnapshotFileResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerPutSnapshotPage implements PUT /api/worker/jobs/{id}/snapshot/{snapshot}/pages/{document_idx}/{page}.png.
func (s *Server) WorkerPutSnapshotPage(ctx context.Context, request WorkerPutSnapshotPageRequestObject) (WorkerPutSnapshotPageResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerPutSnapshotThumb implements PUT /api/worker/jobs/{id}/snapshot/{snapshot}/thumbs/{document_idx}/{page}.png.
func (s *Server) WorkerPutSnapshotThumb(ctx context.Context, request WorkerPutSnapshotThumbRequestObject) (WorkerPutSnapshotThumbResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerPutRevisionRemark implements PUT /api/worker/jobs/{id}/input/revision-{n}/remarks/{idx}.png.
func (s *Server) WorkerPutRevisionRemark(ctx context.Context, request WorkerPutRevisionRemarkRequestObject) (WorkerPutRevisionRemarkResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerCommitSnapshot implements POST /api/worker/jobs/{id}/snapshot/{snapshot}/commit.
func (s *Server) WorkerCommitSnapshot(ctx context.Context, request WorkerCommitSnapshotRequestObject) (WorkerCommitSnapshotResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerQuestion implements POST /api/worker/jobs/{id}/question.
func (s *Server) WorkerQuestion(ctx context.Context, request WorkerQuestionRequestObject) (WorkerQuestionResponseObject, error) {
	return nil, errNotImplemented
}

// WorkerFinish implements POST /api/worker/jobs/{id}/finish.
func (s *Server) WorkerFinish(ctx context.Context, request WorkerFinishRequestObject) (WorkerFinishResponseObject, error) {
	return nil, errNotImplemented
}
