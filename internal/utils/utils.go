package utils

const (
	LabelPrefix    = "nodedrain.k8s.slyng.dk"
	LabelComponent = LabelPrefix + "/component"
)

func PtrTo[T any](v T) *T {
	return &v
}
