package rows

import (
	"slices"
	"strings"
)

// returning lists every column explicitly so the row comes back in the same
// order as the grid columns.
func returning(t Table) string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = col(c.Name)
	}
	return " RETURNING " + strings.Join(cols, ", ")
}

func sortedKeys(m map[string]*string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Insert builds INSERT ... RETURNING. Omitted columns get their DEFAULT.
func Insert(t Table, values map[string]*string) (*Statement, error) {
	st := &Statement{}
	if len(values) == 0 {
		st.SQL = "INSERT INTO " + t.ident() + " DEFAULT VALUES" + returning(t)
		return st, nil
	}
	var cols, vals []string
	for _, name := range sortedKeys(values) {
		c, ok := t.column(name)
		if !ok {
			return nil, invalid("unknown column %q", name)
		}
		cols = append(cols, col(c.Name))
		vals = append(vals, st.bind(values[name], c.TypeOID))
	}
	st.SQL = "INSERT INTO " + t.ident() + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(vals, ", ") + ")" + returning(t)
	return st, nil
}

// Update builds UPDATE ... WHERE pk AND every edited column still holds its
// original value (optimistic check, SPEC §8) RETURNING the row. The check
// compares text representations: the client's original came from the text
// output, and types such as json or point have no equality operator.
func Update(t Table, pk, set, original map[string]*string) (*Statement, error) {
	if len(set) == 0 {
		return nil, invalid("nothing to update")
	}
	st := &Statement{}
	var assigns []string
	for _, name := range sortedKeys(set) {
		c, ok := t.column(name)
		if !ok {
			return nil, invalid("unknown column %q", name)
		}
		assigns = append(assigns, col(c.Name)+" = "+st.bind(set[name], c.TypeOID))
	}
	where, err := PKWhere(t, pk, st)
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(set) {
		orig, ok := original[name]
		if !ok {
			return nil, invalid("original value of %q is required", name)
		}
		c, _ := t.column(name)
		where += " AND " + col(c.Name) + "::text IS NOT DISTINCT FROM " + st.bind(orig, textOID)
	}
	st.SQL = "UPDATE " + t.ident() + " SET " + strings.Join(assigns, ", ") + " WHERE " + where + returning(t)
	return st, nil
}

// Delete builds DELETE ... WHERE pk.
func Delete(t Table, pk map[string]*string) (*Statement, error) {
	st := &Statement{}
	where, err := PKWhere(t, pk, st)
	if err != nil {
		return nil, err
	}
	st.SQL = "DELETE FROM " + t.ident() + " WHERE " + where
	return st, nil
}
