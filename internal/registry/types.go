package registry

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
)

// TypeInfo holds information about a registered type.
type TypeInfo struct {
	Name string
	Type reflect.Type
}

// Registry manages type registration for serialization/deserialization.
type Registry struct {
	mu    sync.RWMutex
	types map[string]reflect.Type
}

// Global registry instance.
var globalRegistry = &Registry{
	types: make(map[string]reflect.Type),
}

// Register registers a type with its fully qualified name.
// Returns the type name and type info.
func Register[T any]() TypeInfo {
	var zero T
	t := reflect.TypeOf(zero)

	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	name := getFullyQualifiedName(t)

	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()

	if existing, ok := globalRegistry.types[name]; ok {
		// Allow re-registration of the same type
		if existing != t {
			panic(fmt.Sprintf("type name collision: %s already registered with different type", name))
		}
	} else {
		globalRegistry.types[name] = t
	}

	return TypeInfo{
		Name: name,
		Type: t,
	}
}

// GetTypeName returns the fully qualified name for a type.
func GetTypeName[T any]() string {
	var zero T
	t := reflect.TypeOf(zero)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return getFullyQualifiedName(t)
}

// Serialize serializes a value to JSON along with its type name.
func Serialize(v interface{}) (typeName string, data []byte, err error) {
	if v == nil {
		return "", nil, nil
	}

	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	typeName = getFullyQualifiedName(t)
	data, err = json.Marshal(v)
	return typeName, data, err
}

// Deserialize deserializes JSON data into a value of the registered type.
func Deserialize(typeName string, data []byte) (interface{}, error) {
	if typeName == "" || len(data) == 0 {
		return nil, nil
	}

	globalRegistry.mu.RLock()
	t, ok := globalRegistry.types[typeName]
	globalRegistry.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown type: %s (not registered)", typeName)
	}

	// Create a pointer to a new instance of the type
	ptr := reflect.New(t)

	if err := json.Unmarshal(data, ptr.Interface()); err != nil {
		return nil, fmt.Errorf("failed to deserialize %s: %w", typeName, err)
	}

	return ptr.Interface(), nil
}

// getFullyQualifiedName returns the fully qualified name of a type.
// Format: "package/path.TypeName"
func getFullyQualifiedName(t reflect.Type) string {
	if t.PkgPath() == "" {
		// Built-in type or unnamed type
		return t.Name()
	}
	return t.PkgPath() + "." + t.Name()
}

// ListRegisteredTypes returns all registered type names (for debugging).
func ListRegisteredTypes() []string {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	names := make([]string, 0, len(globalRegistry.types))
	for name := range globalRegistry.types {
		names = append(names, name)
	}
	return names
}
