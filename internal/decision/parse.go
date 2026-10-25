package decision

import "fmt"

// ParseStaffing turns a raw batch into the staffing answers the policy reads.
// It lives here because the transport attaches no meaning to answers. Anything
// missing, unknown or out of range is an error, never a zero value.
func ParseStaffing(raw map[string]Answer) (Answers, error) {
	need := func(key, wantType string) (Answer, error) {
		a, ok := raw[key]
		if !ok {
			return Answer{}, fmt.Errorf("answer %q missing from the batch", key)
		}
		if a.Type != wantType {
			return Answer{}, fmt.Errorf("answer %q has type %q, want %q", key, a.Type, wantType)
		}
		return a, nil
	}

	shape, err := need(QShape, "choice")
	if err != nil {
		return Answers{}, err
	}
	routine, err := need(QRoutine, "score")
	if err != nil {
		return Answers{}, err
	}
	sec, err := need(QSecurity, "noul")
	if err != nil {
		return Answers{}, err
	}
	des, err := need(QDestructive, "noul")
	if err != nil {
		return Answers{}, err
	}
	ext, err := need(QExternal, "noul")
	if err != nil {
		return Answers{}, err
	}
	par, err := need(QParallel, "noul")
	if err != nil {
		return Answers{}, err
	}

	out := Answers{
		Shape:             Shape(shape.Choice),
		ShapeConfidence:   shape.Confidence,
		RoutineProportion: routine.Score,
		Security:          sec.Noul,
		Destructive:       des.Noul,
		External:          ext.Noul,
		Parallelizable:    par.Noul,
	}
	if err := out.Validate(); err != nil {
		return Answers{}, err
	}
	return out, nil
}
