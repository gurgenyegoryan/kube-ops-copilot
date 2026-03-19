package model

import "time"

type Severity string

type Urgency string

type Confidence string

type AutomationSuitability string

type ApprovalRequirement string

type ExecutionSafety string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

const (
	UrgencyImmediate Urgency = "immediate"
	UrgencyToday     Urgency = "today"
	UrgencyThisWeek  Urgency = "this week"
	UrgencyBacklog   Urgency = "backlog"
)

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

const (
	SuitabilityAdvisoryOnly        AutomationSuitability = "advisory_only"
	SuitabilityApprovalGatedChange AutomationSuitability = "approval_gated_change"
	SuitabilityManualOnly          AutomationSuitability = "manual_only"
)

const (
	ApprovalNone               ApprovalRequirement = "none"
	ApprovalNeedsOperator      ApprovalRequirement = "needs_operator_approval"
	ApprovalHighRiskManualOnly ApprovalRequirement = "high_risk_manual_only"
)

const (
	SafetySafeAutoCandidate     ExecutionSafety = "safe_auto_candidate"
	SafetyNeedsOperatorApproval ExecutionSafety = "needs_operator_approval"
	SafetyHighRiskManualOnly    ExecutionSafety = "high_risk_manual_only"
)

type Finding struct {
	Title                 string                `json:"title"`
	Severity              Severity              `json:"severity"`
	Urgency               Urgency               `json:"urgency"`
	Confidence            Confidence            `json:"confidence"`
	AffectedScope         string                `json:"affectedScope"`
	WhyItMatters          string                `json:"whyItMatters"`
	AutomationSuitability AutomationSuitability `json:"automationSuitability"`
}

type Evidence struct {
	Signal string `json:"signal"`
}

type Hypothesis struct {
	Rank                   int        `json:"rank"`
	Description            string     `json:"description"`
	Probability            Confidence `json:"probability"`
	SupportingEvidence     []Evidence `json:"supportingEvidence"`
	ContradictoryOrMissing []Evidence `json:"contradictoryOrMissingEvidence"`
	WhatToVerifyNext       []string   `json:"whatToVerifyNext"`
}

type Action struct {
	Title               string              `json:"title"`
	ExpectedBenefit     string              `json:"expectedBenefit"`
	RiskTradeoff        string              `json:"riskTradeoff"`
	Priority            string              `json:"priority"`
	ExecutionSafety     ExecutionSafety     `json:"executionSafety"`
	ApprovalRequirement ApprovalRequirement `json:"approvalRequirement"`
	RollbackOutline     string              `json:"rollbackOutline"`
}

type Actions struct {
	Immediate  []Action `json:"immediate"`
	ShortTerm  []Action `json:"shortTerm"`
	Preventive []Action `json:"preventive"`
}

type ExecutionPlan struct {
	TargetResource         string              `json:"targetResource"`
	ExactIntendedChange    string              `json:"exactIntendedChange"`
	SafeExecutionNotes     []string            `json:"safeExecutionNotes"`
	RollbackPlan           []string            `json:"rollbackPlan"`
	PostChangeVerification []string            `json:"postChangeVerification"`
	ExecutionSafety        ExecutionSafety     `json:"executionSafety"`
	ApprovalRequirement    ApprovalRequirement `json:"approvalRequirement"`
}

type SnapshotAssessment struct {
	ProductionReadinessScore int        `json:"productionReadinessScore"`
	OperationalRisk          Severity   `json:"operationalRisk"`
	AutomationConfidence     Confidence `json:"automationConfidence"`
	ObservabilityCoverage    string     `json:"observabilityCoverage"`
	ConfidenceLimiters       []string   `json:"confidenceLimiters"`
	TopRiskThemes            []string   `json:"topRiskThemes"`
}

type Report struct {
	GeneratedAt time.Time `json:"generatedAt"`

	ExecutiveSummary        string             `json:"executiveSummary"`
	Assessment              SnapshotAssessment `json:"assessment"`
	KeyFindings             []Finding          `json:"keyFindings"`
	Evidence                []Evidence         `json:"evidence"`
	Hypotheses              []Hypothesis       `json:"likelyRootCauseHypotheses"`
	Recommended             Actions            `json:"recommendedActions"`
	ProposedOperatorMessage string             `json:"proposedOperatorMessage"`
	ExecutionPlan           *ExecutionPlan     `json:"executionPlan,omitempty"`
	HiddenRisks             []string           `json:"hiddenRisks"`
	Unknowns                []string           `json:"unknowns"`
	FinalVerdict            string             `json:"finalOperatorVerdict"`
}
