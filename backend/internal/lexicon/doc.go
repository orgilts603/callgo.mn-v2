// Package lexicon implements the self-improving correction engine of CallGo.mn.
//
// Admins fix misrecognised words in call transcripts (see PATCH /api/turns/{id});
// each fix becomes a domain.LexiconCorrection that is compiled into a fast,
// Unicode-aware [Matcher] and applied to future STT text. The [Service] adds
// per-organisation caching, event publishing and the export used by the
// Python agent at bootstrap. [DiffWord] derives a suggested correction from a
// turn's original and edited text.
package lexicon
