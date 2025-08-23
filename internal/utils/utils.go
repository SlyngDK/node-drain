package utils

import "fmt"

const (
	LabelPrefix    = "nodedrain.k8s.slyng.dk"
	LabelComponent = LabelPrefix + "/component"
)

func PtrTo[T any](v T) *T {
	return &v
}

func GetFieldOwner(managerNamespace string) string {
	return fmt.Sprintf("%s-manager", managerNamespace)
}
