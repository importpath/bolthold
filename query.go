// Copyright 2016 Tim Shannon. All rights reserved.
// Use of this source code is governed by the MIT license
// that can be found in the LICENSE file.

package bolthold

import (
	"fmt"
	"reflect"
	"regexp"
	"unicode"

	"github.com/boltdb/bolt"
)

const (
	eq = iota //==
	ne        // !=
	gt        // >
	lt        // <
	ge        // >=
	le        // <=
	in
	re // regular expression
	fn // func
)

// Key is shorthand for specifying a query to run again the Key in a bolthold, simply returns ""
// Where(bolthold.Key()).Eq("testkey")
func Key() string {
	return ""
}

// TODO: Allow referencing self in queries
// Where("FirstName").Eq(Field("LastName"))

// Query is a chained collection of criteria of which an object in the bolthold needs to match to be returned
type Query struct {
	index         string
	currentField  string
	fieldCriteria map[string][]*Criterion
	ors           []*Query
	badIndex      bool
}

// IsEmpty returns true if the query is an empty query
// an empty query matches against everything
func (q *Query) IsEmpty() bool {
	if q.index != "" {
		return false
	}
	if len(q.fieldCriteria) != 0 {
		return false
	}

	if q.ors != nil {
		return false
	}

	return true
}

// Criterion is an operator and a value that a given field needs to match on
type Criterion struct {
	query    *Query
	operator int
	value    interface{}
	inValues []interface{}
}

// Where starts a query for specifying the criteria that an object in the bolthold needs to match to
// be returned in a Find result
/*
	Query API Example

	s.Find(Where("Name").Eq("Tim Shannon").And("DOB").Lt(time.Now()).
		Or(Where("Title").Eq("Boss").And("DOB").Lt(time.Now())))


	Since Gobs only encode exported fields, this will panic if you pass in a field with a lower case first letter
*/
func Where(field string) *Criterion {
	if !startsUpper(field) {
		panic("The first letter of a field in a bolthold query must be upper-case")
	}

	return &Criterion{
		query: &Query{
			index:         field,
			currentField:  field,
			fieldCriteria: make(map[string][]*Criterion),
		},
	}
}

// And creates a nother set of criterion the needs to apply to a query
func (q *Query) And(field string) *Criterion {
	if !startsUpper(field) {
		panic("The first letter of a field in a bolthold query must be upper-case")
	}

	q.currentField = field
	return &Criterion{
		query: q,
	}
}

// Or creates another separate query that gets unioned with any other results in the query
func (q *Query) Or(query *Query) *Query {
	q.ors = append(q.ors, query)
	return q
}

func (q *Query) matchesAllFields(key []byte, value reflect.Value) (bool, error) {
	if q.IsEmpty() {
		return true, nil
	}

	for field, criteria := range q.fieldCriteria {
		if field == q.index && !q.badIndex {
			// already handled by index Iterator
			continue
		}

		if field == Key() {
			ok, err := matchesAllCriteria(criteria, key, true)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}

			continue
		}

		//TODO: Allow deep names. struct1.field1.fieldChild
		fVal := value.Elem().FieldByName(field)
		if !fVal.IsValid() {
			return false, fmt.Errorf("The field %s does not exist in the type %s", field, value)
		}

		ok, err := matchesAllCriteria(criteria, fVal.Interface(), false)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}

	return true, nil
}

func (c *Criterion) op(op int, value interface{}) *Query {
	c.operator = op
	c.value = value

	q := c.query
	q.fieldCriteria[q.currentField] = append(q.fieldCriteria[q.currentField], c)

	return q
}

// Eq tests if the current field is Equal to the passed in value
func (c *Criterion) Eq(value interface{}) *Query {
	return c.op(eq, value)
}

// Ne test if the current field is Not Equal to the passed in value
func (c *Criterion) Ne(value interface{}) *Query {
	return c.op(ne, value)
}

// Gt test if the current field is Greater Than the passed in value
func (c *Criterion) Gt(value interface{}) *Query {
	return c.op(gt, value)
}

// Lt test if the current field is Less Than the passed in value
func (c *Criterion) Lt(value interface{}) *Query {
	return c.op(lt, value)
}

// Ge test if the current field is Greater Than or Equal To the passed in value
func (c *Criterion) Ge(value interface{}) *Query {
	return c.op(ge, value)
}

// Le test if the current field is Less Than or Equal To the passed in value
func (c *Criterion) Le(value interface{}) *Query {
	return c.op(le, value)
}

// In test if the current field is a member of the slice of values passed in
func (c *Criterion) In(values ...interface{}) *Query {
	c.operator = in
	c.inValues = values

	q := c.query
	q.fieldCriteria[q.currentField] = append(q.fieldCriteria[q.currentField], c)

	return q
}

// RegExp will test if a field matches against the regular expression
// The Field Value will be converted to string (%s) before testing
func (c *Criterion) RegExp(expression *regexp.Regexp) *Query {
	return c.op(re, expression)
}

// MatchFunc is a function used to test an arbitrary matching value in a query
type MatchFunc func(field interface{}) (bool, error)

// MatchFunc will test if a field matches the passed in function
func (c *Criterion) MatchFunc(match MatchFunc) *Query {
	return c.op(fn, match)
}

// test if the criterion passes with the passed in value
func (c *Criterion) test(testValue interface{}, encoded bool) (bool, error) {
	var value interface{}
	if encoded {
		// used with keys
		value = reflect.New(reflect.TypeOf(c.value)).Interface()
		err := decode(testValue.([]byte), value)
		if err != nil {
			return false, err
		}

	} else {
		value = testValue
	}

	switch c.operator {
	case in:
		for i := range c.inValues {
			result, err := c.compare(value, c.inValues[i])
			if err != nil {
				return false, err
			}
			if result == 0 {
				return true, nil
			}
		}

		return false, nil
	case re:
		return c.value.(*regexp.Regexp).Match([]byte(fmt.Sprintf("%s", value))), nil
	case fn:
		return c.value.(MatchFunc)(value)
	default:
		//comparison operators
		result, err := c.compare(value, c.value)
		if err != nil {
			return false, err
		}

		switch c.operator {
		case eq:
			return result == 0, nil
		case ne:
			return result != 0, nil
		case gt:
			return result > 0, nil
		case lt:
			return result < 0, nil
		case le:
			return result < 0 || result == 0, nil
		case ge:
			return result > 0 || result == 0, nil
		default:
			panic("invalid operator")
		}
	}
}

func matchesAllCriteria(criteria []*Criterion, value interface{}, encoded bool) (bool, error) {
	for i := range criteria {
		ok, err := criteria[i].test(value, encoded)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}

	return true, nil
}

func startsUpper(str string) bool {
	if str == "" {
		return true
	}

	for _, r := range str {
		return unicode.IsUpper(r)
	}

	return false
}

func (q *Query) String() string {
	s := ""

	if q.index != "" {
		s += "Using Index [" + q.index + "] "
	}

	s += "Where "
	for field, criteria := range q.fieldCriteria {
		for i := range criteria {
			s += field + " " + criteria[i].String()
			s += "\n\tAND "
		}
	}

	// remove last AND
	s = s[:len(s)-6]

	for i := range q.ors {
		s += "\nOr " + q.ors[i].String()
	}

	return s
}

func (c *Criterion) String() string {
	s := ""
	switch c.operator {
	case eq:
		s += "=="
	case ne:
		s += "!="
	case gt:
		s += ">"
	case lt:
		s += "<"
	case le:
		s += "<="
	case ge:
		s += ">="
	case in:
		return "in " + fmt.Sprintf("%v", c.inValues)
	case re:
		s += "matches the regular expression"
	case fn:
		s += "matches the function"
	default:
		panic("invalid operator")
	}
	return s + " " + fmt.Sprintf("%v", c.value)
}

func runQuery(tx *bolt.Tx, result interface{}, query *Query, retrievedKeys keyList) error {
	if query == nil {
		query = &Query{}
	}
	resultVal := reflect.ValueOf(result)
	if resultVal.Kind() != reflect.Ptr || resultVal.Elem().Kind() != reflect.Slice {
		panic("result argument must be a slice address")
	}

	sliceVal := resultVal.Elem()
	//sliceVal = sliceVal.Slice(0, 0) // empty slice

	elType := sliceVal.Type().Elem()

	// preserve original type
	oType := elType

	for elType.Kind() == reflect.Ptr {
		elType = elType.Elem()
	}

	iter := newIterator(tx, newStorer(reflect.New(elType).Interface()).Type(), query)

	newKeys := make(keyList, 0)

	for k, v := iter.Next(); k != nil; k, v = iter.Next() {

		if len(retrievedKeys) != 0 {
			// don't check this record if it's already been retrieved
			if retrievedKeys.in(k) {
				continue
			}
		}

		val := reflect.New(elType)

		err := decode(v, val.Interface())
		if err != nil {
			return err
		}

		ok, err := query.matchesAllFields(k, val)
		if err != nil {
			return err
		}

		if ok {
			// add to result
			if oType.Kind() == reflect.Ptr {
				sliceVal = reflect.Append(sliceVal, val)
			} else {
				sliceVal = reflect.Append(sliceVal, val.Elem())
			}
			// track that this key's entry has been added to the result list
			newKeys.add(k)
		}
	}

	if iter.Error() != nil {
		return iter.Error()
	}

	resultVal.Elem().Set(sliceVal.Slice(0, sliceVal.Len()))

	if len(query.ors) > 0 {
		for i := range newKeys {
			retrievedKeys.add(newKeys[i])
		}

		for i := range query.ors {
			err := runQuery(tx, result, query.ors[i], retrievedKeys)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func deleteQuery(tx *bolt.Tx, dataType interface{}, query *Query, deletedKeys keyList) error {
	if query == nil {
		query = &Query{}
	}
	storer := newStorer(dataType)

	for reflect.TypeOf(dataType).Kind() == reflect.Ptr {
		dataType = reflect.ValueOf(dataType).Elem().Interface()
	}

	iter := newIterator(tx, storer.Type(), query)

	newKeys := make(keyList, 0)

	for k, v := iter.Next(); k != nil; k, v = iter.Next() {

		if len(deletedKeys) != 0 {
			// don't check this record if it's already been deleted
			if deletedKeys.in(k) {
				continue
			}
		}

		val := reflect.New(reflect.TypeOf(dataType))

		err := decode(v, val.Interface())
		if err != nil {
			return err
		}

		ok, err := query.matchesAllFields(k, val)
		if err != nil {
			return err
		}

		if ok {
			b := tx.Bucket([]byte(storer.Type()))
			err = b.Delete(k)
			if err != nil {
				return err
			}

			// remove any indexes
			err = indexDelete(storer, tx, k, val.Interface())
			if err != nil {
				return err
			}

			newKeys.add(k)
		}
	}

	if iter.Error() != nil {
		return iter.Error()
	}

	if len(query.ors) > 0 {
		for i := range newKeys {
			deletedKeys.add(newKeys[i])
		}

		for i := range query.ors {
			err := deleteQuery(tx, dataType, query.ors[i], deletedKeys)
			if err != nil {
				return err
			}
		}
	}

	return nil

}
