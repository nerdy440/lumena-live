package moderation

// Catalogue is the public rule catalogue (doc 07 §11's GET /policies/rules).
// Every enforcement's rule_id must reference an entry here — CreateEnforcement
// rejects unknown IDs (ErrRuleNotFound) rather than let a moderator invent
// one on the fly, so "rule_id NOT NULL" (doc 06 §11) actually means
// something rather than being satisfiable with a garbage string.
//
// CS-01/SX-01/VL-01 match streaming.classToRuleID's existing auto-moderation
// labels (Phase 5) — these are the same rules, not a parallel catalogue.
var Catalogue = []ModerationRule{
	{ID: "CS-01", Title: "Child sexual abuse material", Description: "Content matched against the NCMEC hash database.", Severity: 5, PublicURL: "https://lumena.live/policy#CS-01"},
	{ID: "CS-03", Title: "Sexual content in public rooms", Description: "Sexually explicit content in a public broadcast or room.", Severity: 3, PublicURL: "https://lumena.live/policy#CS-03"},
	{ID: "SX-01", Title: "Explicit content", Description: "Explicit content flagged by automated classification.", Severity: 3, PublicURL: "https://lumena.live/policy#SX-01"},
	{ID: "VL-01", Title: "Graphic violence", Description: "Graphic violence flagged by automated classification.", Severity: 3, PublicURL: "https://lumena.live/policy#VL-01"},
	{ID: "HR-01", Title: "Harassment or bullying", Description: "Targeted harassment, threats, or bullying of another user.", Severity: 2, PublicURL: "https://lumena.live/policy#HR-01"},
	{ID: "SP-01", Title: "Spam", Description: "Unsolicited repetitive content or promotion.", Severity: 1, PublicURL: "https://lumena.live/policy#SP-01"},
	{ID: "GR-01", Title: "Grooming pattern", Description: "Behavior consistent with grooming of a minor — escalated to human review.", Severity: 5, PublicURL: "https://lumena.live/policy#GR-01"},
	{ID: "RP-01", Title: "User report — under review", Description: "Action taken following a user report, pending or after moderator triage.", Severity: 2, PublicURL: "https://lumena.live/policy#RP-01"},
}

func RuleByID(id string) (ModerationRule, bool) {
	for _, r := range Catalogue {
		if r.ID == id {
			return r, true
		}
	}
	return ModerationRule{}, false
}

func ValidAction(a EnforcementAction) bool {
	switch a {
	case ActionWarn, ActionMute, ActionRestrictBroadcast, ActionRestrictPV, ActionSuspend, ActionTerminate:
		return true
	default:
		return false
	}
}
