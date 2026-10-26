package stream

// MarkClientAbort records a client disconnect. The caller is gone, so no error
// frame may be written; a completed stream stays completed. This is P24's
// result classification, kept next to the abort contract that the dispatcher
// consumes for partial settlement.
func (r *Result) MarkClientAbort(completed bool) {
	r.ClientAborted = true
	r.NeedErrorFrame = false
	if !completed {
		r.UpstreamTruncated = false
	}
}
