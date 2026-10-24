package cortex

import "github.com/sausheong/cortex/internal/render"

// RenderDecayMarkdown renders a DecayReport as human-readable markdown.
func RenderDecayMarkdown(r DecayReport) string { return render.RenderDecayMarkdown(r) }

// RenderExpireMarkdown renders a ExpireReport as human-readable markdown,
// mirroring RenderDecayMarkdown.
func RenderExpireMarkdown(r ExpireReport) string { return render.RenderExpireMarkdown(r) }

// RenderLintMarkdown formats a LintReport as a human-readable markdown
// document. Exported wrapper for CLI consumption.
func RenderLintMarkdown(r LintReport) string { return render.RenderLintMarkdown(r) }

// RenderMaintainMarkdown renders a MaintainReport by composing the per-pass
// renders under a single header. Sub-passes toggled off (nil) are noted as
// skipped.
func RenderMaintainMarkdown(r MaintainReport) string { return render.RenderMaintainMarkdown(r) }

// RenderProfileMarkdown renders a Profile as a human-readable markdown digest.
func RenderProfileMarkdown(p Profile) string { return render.RenderProfileMarkdown(p) }

// RenderProfileReportMarkdown renders a ProfileReport (the Maintain pass
// summary).
func RenderProfileReportMarkdown(r ProfileReport) string {
	return render.RenderProfileReportMarkdown(r)
}

// RenderReconcileMarkdown renders a ReconcileReport as a human-readable
// markdown summary, mirroring RenderLintMarkdown. It uses dry-run wording
// ("Proposed", "Rejected by gate"); use RenderReconcileMarkdownMode to render
// the applied-path wording.
func RenderReconcileMarkdown(r ReconcileReport) string { return render.RenderReconcileMarkdown(r) }

// RenderReconcileMarkdownMode renders a ReconcileReport as a human-readable
// markdown summary. When applied is false the report describes a dry-run
// ("Proposed Supersessions", "Rejected by gate"); when applied is true the
// report describes the result of an apply run ("Applied Supersessions",
// "Skipped (re-validation)"), where r.Proposed lists what was applied and
// r.Rejected lists re-validation skips.
func RenderReconcileMarkdownMode(r ReconcileReport, applied bool) string {
	return render.RenderReconcileMarkdownMode(r, applied)
}

// RenderRelationMarkdown renders a RelationReport as human-readable markdown,
// mirroring RenderReconcileMarkdown.
func RenderRelationMarkdown(r RelationReport) string { return render.RenderRelationMarkdown(r) }
