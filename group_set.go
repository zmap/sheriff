package sheriff

type groupSet map[string]int

func (s groupSet) incrementGroups(groups []string) {
	for i := range groups {
		s[groups[i]]++
	}
}

func (s groupSet) decrementGroups(groups []string) {
	for i := range groups {
		s[groups[i]]--
	}
}

func (s groupSet) contains(group string) bool {
	return s[group] > 0
}

func (s groupSet) containsAny(groups []string) bool {
	for i := range groups {
		if s.contains(groups[i]) {
			return true
		}
	}
	return false
}
