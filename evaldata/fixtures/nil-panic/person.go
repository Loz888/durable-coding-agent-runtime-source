package example

type Person struct {
	Name string
}

func NameLen(p *Person) int {
	return len(p.Name)
}
