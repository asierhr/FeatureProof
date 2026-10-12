package localvalidation

import "fmt"

// ArgumentError identifies one safe command field and rejection category.
type ArgumentError struct{ Field, Category string }

// Error renders a value-free command diagnostic.
func (e *ArgumentError) Error() string {
	return fmt.Sprintf("invalid argument: %s at %s", e.Category, e.Field)
}

// ParseRequest accepts exactly one value for each v1 flag and performs no I/O.
func ParseRequest(args []string) (Request, error) {
	if len(args) == 0 || args[0] != "validate" {
		return Request{}, &ArgumentError{Field: "subcommand", Category: "required"}
	}
	values := map[string]string{}
	for n := 1; n < len(args); n++ {
		flag := args[n]
		if flag != "--spec" && flag != "--plan" && flag != "--target" {
			return Request{}, &ArgumentError{Field: "arguments", Category: "unknown"}
		}
		if _, exists := values[flag]; exists {
			return Request{}, &ArgumentError{Field: flag, Category: "duplicate"}
		}
		if n+1 >= len(args) || args[n+1] == "" {
			return Request{}, &ArgumentError{Field: flag, Category: "required"}
		}
		values[flag] = args[n+1]
		n++
	}
	for _, flag := range []string{"--spec", "--plan", "--target"} {
		if values[flag] == "" {
			return Request{}, &ArgumentError{Field: flag, Category: "required"}
		}
	}
	return Request{SpecPath: values["--spec"], PlanPath: values["--plan"], Target: values["--target"]}, nil
}
