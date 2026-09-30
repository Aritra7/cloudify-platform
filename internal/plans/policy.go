package plans

import "fmt"

// Policy evaluates cost and exposure guardrails before Terraform is invoked.
type Policy struct {
	AllowedRegions map[string]struct{}
	MaxInstances   int
	AllowPublic    bool
}

func DefaultPolicy() Policy {
	return Policy{
		AllowedRegions: map[string]struct{}{
			"us-central1": {},
			"us-east1":    {},
			"us-west1":    {},
		},
		MaxInstances: 10,
		AllowPublic:  false,
	}
}

func (policy Policy) Evaluate(plan Plan) PolicyDecision {
	violations := make([]Violation, 0)
	if _, allowed := policy.AllowedRegions[plan.Specification.Region]; !allowed {
		violations = append(violations, Violation{
			Code: "region_not_allowed", Message: fmt.Sprintf("region %q is not allowed by policy", plan.Specification.Region),
		})
	}
	if plan.Specification.MaxInstances > policy.MaxInstances {
		violations = append(violations, Violation{
			Code: "instance_limit_exceeded", Message: fmt.Sprintf("max_instances exceeds policy limit of %d", policy.MaxInstances),
		})
	}
	if plan.Specification.AllowUnauthenticated && !policy.AllowPublic {
		violations = append(violations, Violation{
			Code: "public_access_denied", Message: "unauthenticated Cloud Run access is denied by policy",
		})
	}
	return PolicyDecision{Allowed: len(violations) == 0, Violations: violations}
}
