package middleware

// ConsultTaskKind is agent_task_queue.context.type of a consult run
// (DENE-1721): a strong seat answering one question another run asked. The
// answer is its final output, which the daemon reports with its own
// credential, so the run's task token needs no write at all and gets none.
// Same reasoning as the alignment carrier above: the prompt says "only
// read", the credential is what makes it true. Local side effects inside the
// run's empty workdir stay out of reach, as they do for the carrier.
const ConsultTaskKind = "consult"

func isConsultReadOnlyScope(taskKind string) bool {
	return taskKind == ConsultTaskKind
}
