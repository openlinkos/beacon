package web

import "github.com/openlinkos/beacon/internal/store"

func sessionEvidence(r store.SessionRecord) (recording, transcript store.EvidenceState) {
	recording, transcript = r.RecordingState, r.TranscriptState
	if r.Ended.IsZero() {
		if recording == "" {
			recording = store.EvidenceCapturing
		}
		if transcript == "" {
			transcript = store.EvidenceCapturing
		}
	}
	return recording, transcript
}

func (s *Server) sessionEvidenceBadge(state store.EvidenceState) badge {
	switch state {
	case store.EvidenceCapturing:
		return badge{Text: s.t("Capture pending"), Tone: "info"}
	case store.EvidenceComplete:
		return badge{Text: s.t("Recorded")}
	case store.EvidenceTruncated:
		return badge{Text: s.t("Truncated"), Tone: "warning"}
	case store.EvidenceUnavailable:
		return badge{Text: s.t("Unavailable"), Tone: "danger"}
	case store.EvidenceMetadataOnly:
		return badge{Text: s.t("Metadata only")}
	default:
		return badge{Text: s.t("Capture status unknown")}
	}
}

func (s *Server) recordingHint(state store.EvidenceState) string {
	switch state {
	case store.EvidenceCapturing:
		return s.t("Beacon has yet to record the end of this session.")
	case store.EvidenceTruncated:
		return s.t("Beacon saved only part of the recording.")
	case store.EvidenceUnavailable:
		return s.t("Beacon could not save a usable recording.")
	case store.EvidenceMetadataOnly:
		return s.t("SFTP and forwarded connections keep connection details.")
	case "":
		return s.t("This older session has no recording capture status.")
	}
	return ""
}

func (s *Server) transcriptHint(r store.SessionRecord, state store.EvidenceState, empty bool) string {
	switch state {
	case store.EvidenceComplete:
		if empty {
			if r.Kind == "shell" {
				return s.t("The transcript contains no structured commands.")
			}
			return s.t("The transcript contains no command output.")
		}
	case store.EvidenceTruncated:
		return s.t("Only part of the command transcript was saved.")
	case store.EvidenceUnavailable:
		return s.t("The command transcript could not be saved or loaded.")
	case store.EvidenceMetadataOnly:
		if r.Kind == "shell" {
			return s.t("This shell provided no structured command records. Check the recording if available.")
		}
		return s.t("This session type has no command transcript.")
	case "":
		return s.t("This older session has no transcript capture status.")
	}
	return ""
}
