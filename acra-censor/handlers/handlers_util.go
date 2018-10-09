/*
Copyright 2018, Cossack Labs Limited

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package handlers contains all query handlers for AcraCensor:
// blacklist handler, which allows everything and forbids specific query/pattern/table;
// whitelist handler, which allows query/pattern/table and restricts/forbids everything else;
// ignore handler, which allows to ignore any query;
// and querycapture module that logs every unique query to the QueryCapture log.
//
// https://github.com/cossacklabs/acra/wiki/AcraCensor
package handlers

import (
	"bytes"
	"errors"
	log "github.com/sirupsen/logrus"
	"github.com/xwb1989/sqlparser"
	"github.com/xwb1989/sqlparser/dependency/querypb"
	"reflect"
	"strings"
)

// Errors returned during parsing SQL queries.
var (
	ErrQueryNotInWhitelist             = errors.New("query not in whitelist")
	ErrQueryInBlacklist                = errors.New("query in blacklist")
	ErrAccessToForbiddenTableBlacklist = errors.New("query tries to access forbidden table")
	ErrAccessToForbiddenTableWhitelist = errors.New("query tries to access forbidden table")
	ErrBlacklistPatternMatch           = errors.New("query's structure is forbidden")
	ErrWhitelistPatternMismatch        = errors.New("query's structure is forbidden")
	ErrNotImplemented                  = errors.New("not implemented yet")
	ErrPatternSyntaxError              = errors.New("fail to parse specified pattern")
	ErrPatternCheckError               = errors.New("failed to check specified pattern match")
	ErrQuerySyntaxError                = errors.New("fail to parse specified query")
	ErrComplexSerializationError       = errors.New("can't perform complex serialization of queries")
	ErrSingleQueryCaptureError         = errors.New("can't capture single query")
	ErrCantOpenFileError               = errors.New("can't open file to write queries")
	ErrCantReadQueriesFromFileError    = errors.New("can't read queries from file")
	ErrUnexpectedCaptureChannelClose   = errors.New("unexpected channel closing while query logging")
	ErrUnexpectedTypeError             = errors.New("should never appear")
)

const (
	// LogQueryLength is maximum query length for logging to syslog.
	LogQueryLength = 100
	// ValueMask is used to mask real Values from SQL queries before logging to syslog.
	ValueMask = "replaced"
)

// TrimStringToN trims query to N chars.
func TrimStringToN(query string, n int) string {
	if len(query) <= n {
		return query
	}
	return query[:n]
}

// NormalizeAndRedactSQLQuery returns a normalized (lowercases SQL commands) SQL string,
// and redacted SQL string with the params stripped out for display.
// Taken from sqlparser package
func NormalizeAndRedactSQLQuery(sql string) (normalizedQuery string, redactedQuery string, error error) {
	bv := map[string]*querypb.BindVariable{}
	sqlStripped, _ := sqlparser.SplitMarginComments(sql)

	// sometimes queries might have ; at the end, that should be stripped
	sqlStripped = strings.TrimSuffix(sqlStripped, ";")

	stmt, err := sqlparser.Parse(sqlStripped)
	if err != nil {
		return "", "", err
	}

	normalizedQ := sqlparser.String(stmt)

	// redact and mask VALUES
	sqlparser.Normalize(stmt, bv, ValueMask)
	redactedQ := sqlparser.String(stmt)

	return normalizedQ, redactedQ, nil
}

func checkPatternsMatching(patterns []sqlparser.Statement, query string) (bool, error) {
	parsedQuery, err := sqlparser.Parse(query)
	if err != nil {
		log.WithError(err).Errorln("Can't parse query")
		return false, ErrQuerySyntaxError
	}

	for _, pattern := range patterns {
		if checkSinglePatternMatch(parsedQuery, pattern) {
			return true, nil
		}
	}
	return false, nil
}

func checkSinglePatternMatch(query, pattern sqlparser.Statement) bool {
	switch pattern.(type) {
	case *sqlparser.Union:
		return handleUnionStatement(query, pattern)
	case *sqlparser.Select:
		return handleSelectStatement(query, pattern)
	case *sqlparser.Stream:
		return handleStreamStatement(query, pattern)
	case *sqlparser.Insert:
		return handleInsertStatement(query, pattern)
	case *sqlparser.Update:
		return handleUpdateStatement(query, pattern)
	case *sqlparser.Delete:
		return handleDeleteStatement(query, pattern)
	case *sqlparser.Set:
		return handleSetStatement(query, pattern)
	case *sqlparser.DBDDL:
		return handleDBDDLStatement(query, pattern)
	case *sqlparser.DDL:
		return handleDDLStatement(query, pattern)
	case *sqlparser.Show:
		return handleShowStatement(query, pattern)
	case *sqlparser.Use:
		return handleUseStatement(query, pattern)
	case *sqlparser.Begin:
		return handleBeginStatement(query, pattern)
	case *sqlparser.Commit:
		return handleCommitStatement(query, pattern)
	case *sqlparser.Rollback:
		return handleRollbackStatement(query, pattern)
	case *sqlparser.OtherRead:
		return handleOtherReadStatement(query, pattern)
	case *sqlparser.OtherAdmin:
		return handleOtherAdminStatement(query, pattern)
	}
	// unexpected case
	return false
}

//SQL statemnent handlers
func handleUnionStatement(query, pattern sqlparser.Statement) bool {
	match := false
	queryUnionNode, ok := query.(*sqlparser.Union)
	if !ok {
		return false
	}
	patternUnionNode, ok := pattern.(*sqlparser.Union)
	if !ok {
		return false
	}

	// check %%UNION%%
	if reflect.DeepEqual(pattern, UnionPatternStatement) {
		return true
	}

	match = matchUnionLeft(queryUnionNode.Left, patternUnionNode.Left)
	if !match {
		return false
	}
	match = matchUnionRight(queryUnionNode.Right, patternUnionNode.Right)
	if !match {
		return false
	}
	match = matchUnionOrderBy(queryUnionNode.OrderBy, patternUnionNode.OrderBy)
	if !match {
		return false
	}
	match = matchUnionLimit(queryUnionNode.Limit, patternUnionNode.Limit)
	if !match {
		return false
	}
	match = matchUnionLock(queryUnionNode.Lock, patternUnionNode.Lock)
	if !match {
		return false
	}

	return true
}
func handleSelectStatement(query, pattern sqlparser.Statement) bool {
	match := false
	querySelectNode, ok := query.(*sqlparser.Select)
	if !ok {
		return false
	}
	patternSelectNode, ok := pattern.(*sqlparser.Select)
	if !ok {
		return false
	}

	// check %%SELECT%%
	if reflect.DeepEqual(pattern, SelectPatternStatement) {
		return true
	}

	match = matchSelectCache(querySelectNode.Cache, patternSelectNode.Cache)
	if !match {
		return false
	}
	match = matchSelectComments(querySelectNode.Comments, patternSelectNode.Comments)
	if !match {
		return false
	}
	match = matchSelectDistinct(querySelectNode.Distinct, patternSelectNode.Distinct)
	if !match {
		return false
	}
	match = matchSelectHints(querySelectNode.Hints, patternSelectNode.Hints)
	if !match {
		return false
	}
	match = matchSelectSelectExprs(querySelectNode.SelectExprs, patternSelectNode.SelectExprs)
	if !match {
		return false
	}
	match = matchSelectFrom(querySelectNode.From, patternSelectNode.From)
	if !match {
		return false
	}
	match = matchSelectWhere(querySelectNode.Where, patternSelectNode.Where)
	if !match {
		// Check %%WHERE%% pattern
		if isWherePattern(patternSelectNode.Where) {
			return true
		}
		return false
	}
	match = matchSelectGroupBy(querySelectNode.GroupBy, patternSelectNode.GroupBy)
	if !match {
		return false
	}
	match = matchSelectHaving(querySelectNode.Having, patternSelectNode.Having)
	if !match {
		return false
	}
	match = matchSelectOrderBy(querySelectNode.OrderBy, patternSelectNode.OrderBy)
	if !match {
		return false
	}
	match = matchSelectLimit(querySelectNode.Limit, patternSelectNode.Limit)
	if !match {
		return false
	}
	match = matchSelectLock(querySelectNode.Lock, patternSelectNode.Lock)
	if !match {
		return false
	}

	return true
}
func handleStreamStatement(query, pattern sqlparser.Statement) bool {
	// TODO
	_ = query
	_ = pattern
	return false
}
func handleInsertStatement(query, pattern sqlparser.Statement) bool {
	match := false
	queryInsertNode, ok := query.(*sqlparser.Insert)
	if !ok {
		return false
	}
	patternInsertNode, ok := pattern.(*sqlparser.Insert)
	if !ok {
		return false
	}

	// check %%INSERT%%
	if reflect.DeepEqual(pattern, InsertPatternStatement) {
		return true
	}

	match = matchInsertAction(queryInsertNode.Action, patternInsertNode.Action)
	if !match {
		return false
	}
	match = matchInsertComments(queryInsertNode.Comments, patternInsertNode.Comments)
	if !match {
		return false
	}
	match = matchInsertIgnore(queryInsertNode.Ignore, patternInsertNode.Ignore)
	if !match {
		return false
	}
	match = matchInsertTable(queryInsertNode.Table, patternInsertNode.Table)
	if !match {
		return false
	}
	match = matchInsertPartitions(queryInsertNode.Partitions, patternInsertNode.Partitions)
	if !match {
		return false
	}
	match = matchInsertColumns(queryInsertNode.Columns, patternInsertNode.Columns)
	if !match {
		return false
	}
	match = matchInsertRows(queryInsertNode.Rows, patternInsertNode.Rows)
	if !match {
		return false
	}
	match = matchInsertOnDup(queryInsertNode.OnDup, patternInsertNode.OnDup)
	if !match {
		return false
	}
	return false
}
func handleUpdateStatement(query, pattern sqlparser.Statement) bool {
	match := false
	queryUpdateNode, ok := query.(*sqlparser.Update)
	if !ok {
		return false
	}
	patternUpdateNode, ok := pattern.(*sqlparser.Update)
	if !ok {
		return false
	}
	// check %%UPDATE%%
	if reflect.DeepEqual(pattern, UpdatePatternStatement) {
		return true
	}

	match = matchUpdateComments(queryUpdateNode.Comments, patternUpdateNode.Comments)
	if !match {
		return false
	}
	match = matchUpdateTableExprs(queryUpdateNode.TableExprs, patternUpdateNode.TableExprs)
	if !match {
		return false
	}
	match = matchUpdateExprs(queryUpdateNode.Exprs, patternUpdateNode.Exprs)
	if !match {
		return false
	}
	match = matchUpdateWhere(queryUpdateNode.Where, patternUpdateNode.Where)
	if !match {
		return false
	}
	match = matchUpdateOrderBy(queryUpdateNode.OrderBy, patternUpdateNode.OrderBy)
	if !match {
		return false
	}
	match = matchUpdateLimit(queryUpdateNode.Limit, patternUpdateNode.Limit)
	if !match {
		return false
	}

	return true
}
func handleDeleteStatement(query, pattern sqlparser.Statement) bool {
	match := false
	queryDeleteNode, ok := query.(*sqlparser.Delete)
	if !ok {
		return false
	}
	patternDeleteNode, ok := pattern.(*sqlparser.Delete)
	if !ok {
		return false
	}
	// check %%DELETE%%
	if reflect.DeepEqual(pattern, DeletePatternStatement) {
		return true
	}
	match = matchDeleteComments(queryDeleteNode.Comments, patternDeleteNode.Comments)
	if !match {
		return false
	}
	match = matchDeleteTargets(queryDeleteNode.Targets, patternDeleteNode.Targets)
	if !match {
		return false
	}
	match = matchDeleteTableExprs(queryDeleteNode.TableExprs, patternDeleteNode.TableExprs)
	if !match {
		return false
	}
	match = matchDeletePartitions(queryDeleteNode.Partitions, patternDeleteNode.Partitions)
	if !match {
		return false
	}
	match = matchDeleteWhere(queryDeleteNode.Where, patternDeleteNode.Where)
	if !match {
		return false
	}
	match = matchDeleteOrderBy(queryDeleteNode.OrderBy, patternDeleteNode.OrderBy)
	if !match {
		return false
	}
	match = matchDeleteLimit(queryDeleteNode.Limit, patternDeleteNode.Limit)
	if !match {
		return false
	}

	return true
}
func handleSetStatement(query, pattern sqlparser.Statement) bool {
	// TODO
	_ = query
	_ = pattern
	return false
}
func handleDBDDLStatement(query, pattern sqlparser.Statement) bool {
	// TODO
	_ = query
	_ = pattern
	return false
}
func handleDDLStatement(query, pattern sqlparser.Statement) bool {
	// TODO
	_ = query
	_ = pattern
	return false
}
func handleShowStatement(query, pattern sqlparser.Statement) bool {
	// TODO
	_ = query
	_ = pattern
	return false
}
func handleUseStatement(query, pattern sqlparser.Statement) bool {
	// TODO
	_ = query
	_ = pattern
	return false
}
func handleBeginStatement(query, pattern sqlparser.Statement) bool {
	if _, ok := query.(*sqlparser.Begin); ok {
		if _, ok := pattern.(*sqlparser.Begin); ok {
			return true
		}
	}
	return false
}
func handleCommitStatement(query, pattern sqlparser.Statement) bool {
	if _, ok := query.(*sqlparser.Commit); ok {
		if _, ok := pattern.(*sqlparser.Commit); ok {
			return true
		}
	}
	return false
}
func handleRollbackStatement(query, pattern sqlparser.Statement) bool {
	if _, ok := query.(*sqlparser.Rollback); ok {
		if _, ok := pattern.(*sqlparser.Rollback); ok {
			return true
		}
	}
	return false
}
func handleOtherReadStatement(query, pattern sqlparser.Statement) bool {
	// TODO
	if _, ok := query.(*sqlparser.OtherRead); ok {
		if _, ok := pattern.(*sqlparser.OtherRead); ok {
			return true
		}
	}
	return false
}
func handleOtherAdminStatement(query, pattern sqlparser.Statement) bool {
	// TODO
	if _, ok := query.(*sqlparser.OtherAdmin); ok {
		if _, ok := pattern.(*sqlparser.OtherAdmin); ok {
			return true
		}
	}
	return false
}

//Select statement matchers
func matchSelectCache(query, pattern string) bool {
	return strings.EqualFold(query, pattern)
}
func matchSelectComments(query, pattern sqlparser.Comments) bool {
	if len(query) != len(pattern) {
		return false
	}
	for index := range pattern {
		if !bytes.Equal(query[index], pattern[index]) {
			return false
		}
	}
	return true
}
func matchSelectDistinct(query, pattern string) bool {
	return strings.EqualFold(query, pattern)
}
func matchSelectHints(query, pattern string) bool {
	return strings.EqualFold(query, pattern)
}
func matchSelectSelectExprs(query, pattern sqlparser.SelectExprs) bool {
	// check star (all columns are allowed)
	if isStarExpr(pattern) {
		return true
	}
	if len(query) != len(pattern) {
		return false
	}
	for index := range pattern {
		if !areEqualSelectExpr(query[index], pattern[index]) {
			return false
		}
	}
	return true
}
func matchSelectFrom(query, pattern sqlparser.TableExprs) bool {
	if len(query) != len(pattern) {
		return false
	}
	for index := range pattern {
		if !areEqualTableExpr(query[index], pattern[index]) {
			return false
		}
	}
	return true
}
func matchSelectWhere(query, pattern *sqlparser.Where) bool {
	if query == nil && pattern == nil {
		return true
	}
	if query == nil || pattern == nil {
		return false
	}
	if !strings.EqualFold(query.Type, pattern.Type) {
		return false
	}
	if !areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	return true
}
func matchSelectHaving(query, pattern *sqlparser.Where) bool {
	return matchSelectWhere(query, pattern)
}
func matchSelectGroupBy(query, pattern sqlparser.GroupBy) bool {
	if len(query) != len(pattern) {
		return false
	}
	for index := range pattern {
		if !areEqualExpr(query[index], pattern[index]) {
			return false
		}
	}
	return true
}
func matchSelectLimit(query, pattern *sqlparser.Limit) bool {
	if query == nil && pattern == nil {
		return true
	}
	if query == nil || pattern == nil {
		return false
	}

	if !areEqualExpr(query.Offset, pattern.Offset) {
		return false
	}
	if !areEqualExpr(query.Rowcount, pattern.Rowcount) {
		return false
	}
	return true
}
func matchSelectOrderBy(query, pattern sqlparser.OrderBy) bool {
	if len(query) != len(pattern) {
		return false
	}
	for index := range pattern {
		if !areEqualExpr(query[index].Expr, pattern[index].Expr) {
			return false
		}
		if !strings.EqualFold(query[index].Direction, pattern[index].Direction) {
			return false
		}
	}
	return true
}
func matchSelectLock(query, pattern string) bool {
	return strings.EqualFold(query, pattern)
}

//Union statement matchers
func matchUnionLeft(query, pattern sqlparser.SelectStatement) bool {
	return areEqualSelectStatement(query, pattern)
}
func matchUnionRight(query, pattern sqlparser.SelectStatement) bool {
	return areEqualSelectStatement(query, pattern)
}
func matchUnionOrderBy(query, pattern sqlparser.OrderBy) bool {
	return matchSelectOrderBy(query, pattern)
}
func matchUnionLimit(query, pattern *sqlparser.Limit) bool {
	return matchSelectLimit(query, pattern)
}
func matchUnionLock(query, pattern string) bool {
	return matchSelectLock(query, pattern)
}

//Insert statement matchers
func matchInsertAction(query, handler string) bool {
	return strings.EqualFold(query, handler)
}
func matchInsertComments(query, pattern sqlparser.Comments) bool {
	return matchSelectComments(query, pattern)
}
func matchInsertIgnore(query, pattern string) bool {
	return strings.EqualFold(query, pattern)
}
func matchInsertTable(query sqlparser.TableName, pattern sqlparser.TableName) bool {
	return areEqualTableName(query, pattern)
}
func matchInsertPartitions(query, pattern sqlparser.Partitions) bool {
	return areEqualPartitions(query, pattern)
}
func matchInsertColumns(query, pattern sqlparser.Columns) bool {
	if len(query) != len(pattern) {
		return false
	}
	for index := range pattern {
		if !areEqualColIdent(query[index], pattern[index]) {
			return false
		}
	}
	return true
}
func matchInsertRows(query, pattern sqlparser.InsertRows) bool {
	switch pattern.(type) {
	case *sqlparser.Select:
		querySelect, ok := query.(*sqlparser.Select)
		if !ok {
			return false
		}
		if !handleSelectStatement(querySelect, pattern.(*sqlparser.Select)) {
			return false
		}
	case *sqlparser.Union:
		queryUnion, ok := query.(*sqlparser.Union)
		if !ok {
			return false
		}
		if !handleUnionStatement(queryUnion, pattern.(*sqlparser.Union)) {
			return false
		}

	case sqlparser.Values:
		queryValues, ok := query.(sqlparser.Values)
		if !ok {
			return false
		}
		patternValues := pattern.(sqlparser.Values)
		if len(queryValues) != len(patternValues) {
			return false
		}
		for index := range pattern.(sqlparser.Values) {
			if !areEqualValTuple(queryValues[index], patternValues[index]) {
				return false
			}
		}

	case *sqlparser.ParenSelect:
		queryParenSelect, ok := query.(*sqlparser.ParenSelect)
		if !ok {
			return false
		}
		if !handleSelectStatement(queryParenSelect.Select, pattern.(*sqlparser.ParenSelect).Select) {
			return false
		}
	default:
		// unexpected
		return false
	}

	return true
}
func matchInsertOnDup(query, pattern sqlparser.OnDup) bool {
	if len(query) != len(pattern) {
		return false
	}

	for index := range pattern {
		if !areEqualExpr(query[index].Expr, pattern[index].Expr) {
			return false
		}
		if !areEqualColName(query[index].Name, pattern[index].Name) {
			return false
		}
	}

	return true
}

//Update statement matchers
func matchUpdateLimit(query *sqlparser.Limit, pattern *sqlparser.Limit) bool {
	return matchSelectLimit(query, pattern)
}
func matchUpdateOrderBy(query, pattern sqlparser.OrderBy) bool {
	return matchSelectOrderBy(query, pattern)
}
func matchUpdateWhere(query, pattern *sqlparser.Where) bool {
	return matchSelectWhere(query, pattern)
}
func matchUpdateExprs(query, pattern sqlparser.UpdateExprs) bool {
	if len(query) != len(pattern) {
		return false
	}
	for index := range pattern {
		if !areEqualUpdateExpr(query[index], pattern[index]) {
			return false
		}
	}
	return true
}
func matchUpdateTableExprs(query, pattern sqlparser.TableExprs) bool {
	return matchSelectFrom(query, pattern)
}
func matchUpdateComments(query, pattern sqlparser.Comments) bool {
	return matchSelectComments(query, pattern)
}

//Delete statement matchers
func matchDeleteLimit(query *sqlparser.Limit, pattern *sqlparser.Limit) bool {
	return matchSelectLimit(query, pattern)
}
func matchDeleteOrderBy(query sqlparser.OrderBy, pattern sqlparser.OrderBy) bool {
	return matchSelectOrderBy(query, pattern)
}
func matchDeleteWhere(query *sqlparser.Where, pattern *sqlparser.Where) bool {
	return matchSelectWhere(query, pattern)
}
func matchDeletePartitions(query sqlparser.Partitions, pattern sqlparser.Partitions) bool {
	return matchInsertPartitions(query, pattern)
}
func matchDeleteTableExprs(query sqlparser.TableExprs, pattern sqlparser.TableExprs) bool {
	return matchSelectFrom(query, pattern)
}
func matchDeleteTargets(query sqlparser.TableNames, pattern sqlparser.TableNames) bool {
	if len(query) != len(pattern) {
		return false
	}
	for index := range pattern {
		if !areEqualTableName(query[index], pattern[index]) {
			return false
		}
	}
	return true
}
func matchDeleteComments(query, pattern sqlparser.Comments) bool {
	return matchSelectComments(query, pattern)
}

// Type comparators
func areEqualTableExpr(query, pattern sqlparser.TableExpr) bool {
	switch pattern.(type) {
	case *sqlparser.AliasedTableExpr:
		queryAliasedTableExpr, ok := query.(*sqlparser.AliasedTableExpr)
		if !ok {
			return false
		}
		if !areEqualAliasedTableExpr(queryAliasedTableExpr, pattern.(*sqlparser.AliasedTableExpr)) {
			return false
		}

	case *sqlparser.JoinTableExpr:
		queryJoinTableExpr, ok := query.(*sqlparser.JoinTableExpr)
		if !ok {
			return false
		}
		if !areEqualJoinTableExpr(queryJoinTableExpr, pattern.(*sqlparser.JoinTableExpr)) {
			return false
		}

	case *sqlparser.ParenTableExpr:
		queryParenTableExpr, ok := query.(*sqlparser.ParenTableExpr)
		if !ok {
			return false
		}
		if !areEqualParenTableExpr(queryParenTableExpr, pattern.(*sqlparser.ParenTableExpr)) {
			return false
		}
	default:
		// unexpected
		return false
	}

	return true
}
func areEqualParenTableExpr(query, pattern *sqlparser.ParenTableExpr) bool {
	if len(query.Exprs) != len(pattern.Exprs) {
		return false
	}
	for index := range pattern.Exprs {
		if !areEqualTableExpr(query.Exprs[index], pattern.Exprs[index]) {
			return false
		}
	}
	return true
}
func areEqualJoinTableExpr(query, pattern *sqlparser.JoinTableExpr) bool {
	if !areEqualJoinConditions(query.Condition, pattern.Condition) {
		return false
	}
	if !strings.EqualFold(query.Join, pattern.Join) {
		return false
	}
	if !areEqualTableExpr(query.LeftExpr, pattern.LeftExpr) {
		return false
	}
	if !areEqualTableExpr(query.RightExpr, pattern.RightExpr) {
		return false
	}
	return true
}
func areEqualAliasedTableExpr(query, pattern *sqlparser.AliasedTableExpr) bool {
	if !areEqualSimpleTableExpr(query.Expr, pattern.Expr) {
		return false
	}
	if !areEqualPartitions(query.Partitions, pattern.Partitions) {
		return false
	}
	if !areEqualTableIdent(query.As, pattern.As) {
		return false
	}
	if !areEqualIndexHints(query.Hints, pattern.Hints) {
		return false
	}
	return true
}
func areEqualPartitions(query, pattern sqlparser.Partitions) bool {
	if len(query) != len(pattern) {
		return false
	}
	for index := range pattern {
		if !areEqualColIdent(query[index], pattern[index]) {
			return false
		}
	}
	return true
}
func areEqualIndexHints(query, pattern *sqlparser.IndexHints) bool {
	if query == nil && pattern == nil {
		return true
	}
	if query == nil || pattern == nil {
		return false
	}

	if len(query.Indexes) != len(pattern.Indexes) {
		return false
	}
	if !strings.EqualFold(query.Type, pattern.Type) {
		return false
	}
	for index := range pattern.Indexes {
		if !areEqualColIdent(query.Indexes[index], pattern.Indexes[index]) {
			return false
		}
	}
	return true
}
func areEqualSimpleTableExpr(query, pattern sqlparser.SimpleTableExpr) bool {
	switch pattern.(type) {
	case sqlparser.TableName:
		queryTableName, ok := query.(sqlparser.TableName)
		if !ok {
			return false
		}
		if !areEqualTableName(queryTableName, pattern.(sqlparser.TableName)) {
			return false
		}
	case *sqlparser.Subquery:
		querySubquery, ok := query.(*sqlparser.Subquery)
		if !ok {
			return false
		}
		if !areEqualSubquery(querySubquery, pattern.(*sqlparser.Subquery)) {
			return false
		}
	default:
		// unexpected
		return false
	}
	return true
}
func areEqualSelectExpr(query, pattern sqlparser.SelectExpr) bool {
	switch pattern.(type) {
	case *sqlparser.StarExpr:
		queryStarExpr, ok := query.(*sqlparser.StarExpr)
		if !ok {
			return false
		}
		return areEqualTableName(queryStarExpr.TableName, pattern.(*sqlparser.StarExpr).TableName)
	case *sqlparser.AliasedExpr:
		// check %%COLUMN%% pattern
		queryAliasedExpr, ok := query.(*sqlparser.AliasedExpr)
		if !ok {
			switch query.(type) {
			case *sqlparser.StarExpr:
				switch pattern.(*sqlparser.AliasedExpr).Expr.(type) {
				case *sqlparser.ColName:
					if isColumnPattern(pattern.(*sqlparser.AliasedExpr).Expr.(*sqlparser.ColName).Name) {
						return true
					}
				}
			}
			return false
		}
		if !areEqualAliasedExpr(queryAliasedExpr, pattern.(*sqlparser.AliasedExpr)) {
			return false
		}
	case sqlparser.Nextval:
		queryNextval, ok := query.(sqlparser.Nextval)
		if !ok {
			return false
		}
		if !areEqualNextval(queryNextval, pattern.(sqlparser.Nextval)) {
			return false
		}
	default:
		// unexpected
		return false
	}
	return true
}
func areEqualNextval(query, pattern sqlparser.Nextval) bool {
	return areEqualExpr(query.Expr, pattern.Expr)
}
func areEqualTableName(query, pattern sqlparser.TableName) bool {
	if !areEqualTableIdent(query.Name, pattern.Name) {
		return false
	}
	if !areEqualTableIdent(query.Qualifier, pattern.Qualifier) {
		return false
	}
	return true
}
func areEqualTableIdent(query, pattern sqlparser.TableIdent) bool {
	return strings.EqualFold(query.CompliantName(), pattern.CompliantName())
}
func areEqualAliasedExpr(query, pattern *sqlparser.AliasedExpr) bool {
	if !areEqualColIdent(query.As, pattern.As) {
		return false
	}
	if !areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	return true
}
func areEqualExpr(query, pattern sqlparser.Expr) bool {
	if query == nil && pattern == nil {
		return true
	}
	if query == nil || pattern == nil {
		return false
	}

	switch pattern.(type) {
	case *sqlparser.AndExpr:
		queryAndExpr, ok := query.(*sqlparser.AndExpr)
		if !ok {
			return false
		}
		if !areEqualAndExpr(queryAndExpr, pattern.(*sqlparser.AndExpr)) {
			return false
		}
	case *sqlparser.OrExpr:
		queryOrExpr, ok := query.(*sqlparser.OrExpr)
		if !ok {
			return false
		}
		if !areEqualOrExpr(queryOrExpr, pattern.(*sqlparser.OrExpr)) {
			return false
		}
	case *sqlparser.NotExpr:
		queryNotExpr, ok := query.(*sqlparser.NotExpr)
		if !ok {
			return false
		}
		if !areEqualNotExpr(queryNotExpr, pattern.(*sqlparser.NotExpr)) {
			return false
		}
	case *sqlparser.ParenExpr:
		queryParenExpr, ok := query.(*sqlparser.ParenExpr)
		if !ok {
			return false
		}
		if !areEqualParenExpr(queryParenExpr, pattern.(*sqlparser.ParenExpr)) {
			return false
		}
	case *sqlparser.ComparisonExpr:
		queryComparisonExpr, ok := query.(*sqlparser.ComparisonExpr)
		if !ok {
			return false
		}
		if !areEqualComparisonExpr(queryComparisonExpr, pattern.(*sqlparser.ComparisonExpr)) {
			return false
		}
	case *sqlparser.RangeCond:
		queryRangeCond, ok := query.(*sqlparser.RangeCond)
		if !ok {
			return false
		}
		if !areEqualRangeCond(queryRangeCond, pattern.(*sqlparser.RangeCond)) {
			return false
		}
	case *sqlparser.IsExpr:
		queryIsExpr, ok := query.(*sqlparser.IsExpr)
		if !ok {
			return false
		}
		if !areEqualIsExpr(queryIsExpr, pattern.(*sqlparser.IsExpr)) {
			return false
		}
	case *sqlparser.ExistsExpr:
		queryExistsExpr, ok := query.(*sqlparser.ExistsExpr)
		if !ok {
			return false
		}
		if !areEqualExistsExpr(queryExistsExpr, pattern.(*sqlparser.ExistsExpr)) {
			return false
		}
	case *sqlparser.SQLVal:
		// check %%VALUE%% and %%LIST_OF_VALUES%% patterns
		// if %%VALUE%% pattern should mask value of node
		// return true for any literal values, boolean and null
		// return false on other values like subqueries
		querySQLVal, ok := query.(*sqlparser.SQLVal)
		if !ok {
			switch query.(type) {
			case sqlparser.BoolVal, *sqlparser.NullVal, *sqlparser.FuncExpr:
				if isValuePattern(pattern.(*sqlparser.SQLVal)) {
					return true
				}
				if isListOfValuesPattern(pattern.(*sqlparser.SQLVal)) {
					return true
				}
			}
			return false
		}
		if !areEqualSQLVal(querySQLVal, pattern.(*sqlparser.SQLVal)) {
			return false
		}
	case *sqlparser.NullVal:
		queryNullVal, ok := query.(*sqlparser.NullVal)
		if !ok {
			return false
		}
		if !areEqualNullVal(queryNullVal, pattern.(*sqlparser.NullVal)) {
			return false
		}
	case sqlparser.BoolVal:
		queryBoolVal, ok := query.(sqlparser.BoolVal)
		if !ok {
			return false
		}
		if !areEqualBoolVal(queryBoolVal, pattern.(sqlparser.BoolVal)) {
			return false
		}
	case *sqlparser.ColName:
		// check %%COLUMN%% pattern
		queryColName, ok := query.(*sqlparser.ColName)
		if !ok {
			switch query.(type) {
			case *sqlparser.SQLVal, *sqlparser.Subquery, *sqlparser.FuncExpr, *sqlparser.CaseExpr, *sqlparser.ParenExpr:
				if isColumnPattern(pattern.(*sqlparser.ColName).Name) {
					return true
				}
			}
			return false
		}
		if !areEqualColName(queryColName, pattern.(*sqlparser.ColName)) {
			return false
		}
	case sqlparser.ValTuple:
		queryValTuple, ok := query.(sqlparser.ValTuple)
		if !ok {
			return false
		}
		if !areEqualValTuple(queryValTuple, pattern.(sqlparser.ValTuple)) {
			return false
		}
	case *sqlparser.Subquery:
		querySubquery, ok := query.(*sqlparser.Subquery)
		if !ok {
			return false
		}
		if !areEqualSubquery(querySubquery, pattern.(*sqlparser.Subquery)) {
			return false
		}
	case sqlparser.ListArg:
		queryListArg, ok := query.(sqlparser.ListArg)
		if !ok {
			return false
		}
		if !areEqualListArg(queryListArg, pattern.(sqlparser.ListArg)) {
			return false
		}
	case *sqlparser.BinaryExpr:
		queryBinaryExpr, ok := query.(*sqlparser.BinaryExpr)
		if !ok {
			return false
		}
		if !areEqualBinaryExpr(queryBinaryExpr, pattern.(*sqlparser.BinaryExpr)) {
			return false
		}
	case *sqlparser.UnaryExpr:
		queryUnaryExpr, ok := query.(*sqlparser.UnaryExpr)
		if !ok {
			return false
		}
		if !areEqualUnaryExpr(queryUnaryExpr, pattern.(*sqlparser.UnaryExpr)) {
			return false
		}
	case *sqlparser.IntervalExpr:
		queryIntervalExpr, ok := query.(*sqlparser.IntervalExpr)
		if !ok {
			return false
		}
		if !areEqualIntervalExpr(queryIntervalExpr, pattern.(*sqlparser.IntervalExpr)) {
			return false
		}
	case *sqlparser.CollateExpr:
		queryCollateExpr, ok := query.(*sqlparser.CollateExpr)
		if !ok {
			return false
		}
		if !areEqualCollateExpr(queryCollateExpr, pattern.(*sqlparser.CollateExpr)) {
			return false
		}
	case *sqlparser.FuncExpr:
		queryFuncExpr, ok := query.(*sqlparser.FuncExpr)
		if !ok {
			return false
		}
		if !areEqualFuncExpr(queryFuncExpr, pattern.(*sqlparser.FuncExpr)) {
			return false
		}
	case *sqlparser.CaseExpr:
		queryCaseExpr, ok := query.(*sqlparser.CaseExpr)
		if !ok {
			return false
		}
		if !areEqualCaseExpr(queryCaseExpr, pattern.(*sqlparser.CaseExpr)) {
			return false
		}
	case *sqlparser.ValuesFuncExpr:
		queryValuesFuncExpr, ok := query.(*sqlparser.ValuesFuncExpr)
		if !ok {
			return false
		}
		if !areEqualValuesFuncExpr(queryValuesFuncExpr, pattern.(*sqlparser.ValuesFuncExpr)) {
			return false
		}
	case *sqlparser.ConvertExpr:
		queryValuesConvertExpr, ok := query.(*sqlparser.ConvertExpr)
		if !ok {
			return false
		}
		if !areEqualConvertExpr(queryValuesConvertExpr, pattern.(*sqlparser.ConvertExpr)) {
			return false
		}
	case *sqlparser.SubstrExpr:
		querySubstrExpr, ok := query.(*sqlparser.SubstrExpr)
		if !ok {
			return false
		}
		if !areEqualSubstrExpr(querySubstrExpr, pattern.(*sqlparser.SubstrExpr)) {
			return false
		}
	case *sqlparser.ConvertUsingExpr:
		queryConvertUsingExpr, ok := query.(*sqlparser.ConvertUsingExpr)
		if !ok {
			return false
		}
		if !areEqualConvertUsingExpr(queryConvertUsingExpr, pattern.(*sqlparser.ConvertUsingExpr)) {
			return false
		}
	case *sqlparser.MatchExpr:
		queryMatchExpr, ok := query.(*sqlparser.MatchExpr)
		if !ok {
			return false
		}
		if !areEqualMatchExpr(queryMatchExpr, pattern.(*sqlparser.MatchExpr)) {
			return false
		}
	case *sqlparser.GroupConcatExpr:
		queryGroupConcatExpr, ok := query.(*sqlparser.GroupConcatExpr)
		if !ok {
			return false
		}
		if !areEqualGroupConcatExpr(queryGroupConcatExpr, pattern.(*sqlparser.GroupConcatExpr)) {
			return false
		}
	case *sqlparser.Default:
		queryDefault, ok := query.(*sqlparser.Default)
		if !ok {
			return false
		}
		if !strings.EqualFold(queryDefault.ColName, pattern.(*sqlparser.Default).ColName) {
			return false
		}
	default:
		// unexpected
		return false
	}

	return true
}
func areEqualGroupConcatExpr(query, pattern *sqlparser.GroupConcatExpr) bool {
	if !strings.EqualFold(query.Distinct, pattern.Distinct) {
		return false
	}
	if !strings.EqualFold(query.Separator, pattern.Separator) {
		return false
	}
	if !matchSelectSelectExprs(query.Exprs, pattern.Exprs) {
		return false
	}
	if !matchSelectOrderBy(query.OrderBy, pattern.OrderBy) {
		return false
	}
	return true
}
func areEqualMatchExpr(query, pattern *sqlparser.MatchExpr) bool {
	if !strings.EqualFold(query.Option, pattern.Option) {
		return false
	}
	if !areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}

	if !matchSelectSelectExprs(query.Columns, pattern.Columns) {
		return false
	}
	return true
}
func areEqualConvertUsingExpr(query, pattern *sqlparser.ConvertUsingExpr) bool {
	if !strings.EqualFold(query.Type, pattern.Type) {
		return false
	}
	if areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	return true
}
func areEqualSubstrExpr(query, pattern *sqlparser.SubstrExpr) bool {
	if !areEqualExpr(query.To, pattern.To) {
		return false
	}
	if !areEqualExpr(query.From, pattern.From) {
		return false
	}
	if !areEqualColName(query.Name, pattern.Name) {
		return false
	}
	return true
}
func areEqualConvertExpr(query, pattern *sqlparser.ConvertExpr) bool {
	if !areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	if !areEqualConvertType(query.Type, pattern.Type) {
		return false
	}
	return true
}
func areEqualConvertType(query, pattern *sqlparser.ConvertType) bool {
	if !strings.EqualFold(query.Type, pattern.Type) {
		return false
	}
	if !strings.EqualFold(query.Charset, pattern.Charset) {
		return false
	}
	if !strings.EqualFold(query.Operator, pattern.Operator) {
		return false
	}
	if areEqualSQLVal(query.Length, pattern.Length) {
		return false
	}
	if areEqualSQLVal(query.Scale, pattern.Scale) {
		return false
	}
	return true
}
func areEqualValuesFuncExpr(query, pattern *sqlparser.ValuesFuncExpr) bool {
	return areEqualColName(query.Name, pattern.Name)
}
func areEqualCaseExpr(query, pattern *sqlparser.CaseExpr) bool {
	if !areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	if !areEqualExpr(query.Else, pattern.Expr) {
		return false
	}

	if len(query.Whens) != len(pattern.Whens) {
		return false
	}

	for index := range pattern.Whens {
		if !areEqualWhen(query.Whens[index], pattern.Whens[index]) {
			return false
		}
	}
	return true
}
func areEqualWhen(query *sqlparser.When, pattern *sqlparser.When) bool {
	if !areEqualExpr(query.Val, pattern.Val) {
		return false
	}
	if !areEqualExpr(query.Cond, pattern.Cond) {
		return false
	}
	return true
}
func areEqualFuncExpr(query, pattern *sqlparser.FuncExpr) bool {
	if query.Distinct != pattern.Distinct {
		return false
	}
	if !areEqualColIdent(query.Name, pattern.Name) {
		return false
	}
	if !areEqualTableIdent(query.Qualifier, pattern.Qualifier) {
		return false
	}
	if !matchSelectSelectExprs(query.Exprs, pattern.Exprs) {
		return false
	}
	return true
}
func areEqualCollateExpr(query, pattern *sqlparser.CollateExpr) bool {
	if !strings.EqualFold(query.Charset, pattern.Charset) {
		return false
	}
	if !areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	return true
}
func areEqualIntervalExpr(query, pattern *sqlparser.IntervalExpr) bool {
	if !strings.EqualFold(query.Unit, pattern.Unit) {
		return false
	}
	if areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	return true
}
func areEqualUnaryExpr(query *sqlparser.UnaryExpr, pattern *sqlparser.UnaryExpr) bool {
	if !strings.EqualFold(query.Operator, pattern.Operator) {
		return false
	}
	if !areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	return true
}
func areEqualBinaryExpr(query, pattern *sqlparser.BinaryExpr) bool {
	if !strings.EqualFold(query.Operator, pattern.Operator) {
		return false
	}
	if !areEqualExpr(query.Left, pattern.Left) {
		return false
	}
	if !areEqualExpr(query.Right, pattern.Right) {
		return false
	}
	return true
}
func areEqualListArg(query sqlparser.ListArg, pattern sqlparser.ListArg) bool {
	return bytes.Equal(query, pattern)
}
func areEqualBoolVal(query, pattern sqlparser.BoolVal) bool {
	return query == pattern
}
func areEqualNullVal(query, pattern *sqlparser.NullVal) bool {
	return reflect.DeepEqual(query, pattern)
}
func areEqualSQLVal(query, pattern *sqlparser.SQLVal) bool {
	if isValuePattern(pattern) {
		return true
	}
	if isListOfValuesPattern(pattern) {
		return true
	}
	if query.Type == pattern.Type && bytes.Equal(query.Val, pattern.Val) {
		return true
	}
	return false
}
func areEqualExistsExpr(query, pattern *sqlparser.ExistsExpr) bool {
	return areEqualSubquery(query.Subquery, pattern.Subquery)
}
func areEqualIsExpr(query, pattern *sqlparser.IsExpr) bool {
	if !strings.EqualFold(query.Operator, pattern.Operator) {
		return false
	}
	if !areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	return true
}
func areEqualRangeCond(query, pattern *sqlparser.RangeCond) bool {
	if !strings.EqualFold(query.Operator, pattern.Operator) {
		return false
	}
	if !areEqualExpr(query.Left, pattern.Left) {
		return false
	}
	if !areEqualExpr(query.From, pattern.From) {
		return false
	}
	if !areEqualExpr(query.To, pattern.To) {
		return false
	}
	return true
}
func areEqualComparisonExpr(query, pattern *sqlparser.ComparisonExpr) bool {
	if !strings.EqualFold(query.Operator, pattern.Operator) {
		return false
	}
	if !areEqualExpr(query.Escape, pattern.Escape) {
		return false
	}
	if !areEqualExpr(query.Left, pattern.Left) {
		return false
	}
	if !areEqualExpr(query.Right, pattern.Right) {
		return false
	}
	return true
}
func areEqualParenExpr(query, pattern *sqlparser.ParenExpr) bool {
	return areEqualExpr(query.Expr, pattern.Expr)
}
func areEqualNotExpr(query, pattern *sqlparser.NotExpr) bool {
	return areEqualExpr(query.Expr, pattern.Expr)
}
func areEqualOrExpr(query, pattern *sqlparser.OrExpr) bool {
	if !areEqualExpr(query.Right, pattern.Right) {
		return false
	}
	if !areEqualExpr(query.Left, pattern.Left) {
		return false
	}
	return true
}
func areEqualAndExpr(query, pattern *sqlparser.AndExpr) bool {
	if !areEqualExpr(query.Right, pattern.Right) {
		return false
	}
	if !areEqualExpr(query.Left, pattern.Left) {
		return false
	}
	return true
}
func areEqualColIdent(query, pattern sqlparser.ColIdent) bool {
	if isColumnPattern(pattern) {
		return true
	}
	return query.Equal(pattern)
}
func areEqualJoinConditions(query, pattern sqlparser.JoinCondition) bool {
	if !areEqualExpr(query.On, pattern.On) {
		return false
	}
	if len(query.Using) != len(pattern.Using) {
		return false
	}
	for index := range pattern.Using {
		if !areEqualColIdent(query.Using[index], pattern.Using[index]) {
			return false
		}
	}
	return true
}
func areEqualSelectStatement(query, pattern sqlparser.SelectStatement) bool {
	switch pattern.(type) {
	case *sqlparser.Select:
		querySelect, ok := query.(*sqlparser.Select)
		if !ok {
			return false
		}
		if !handleSelectStatement(querySelect, pattern.(*sqlparser.Select)) {
			return false
		}
	case *sqlparser.Union:
		queryUnion, ok := query.(*sqlparser.Union)
		if !ok {
			return false
		}
		if !handleUnionStatement(queryUnion, pattern.(*sqlparser.Union)) {
			return false
		}
	case *sqlparser.ParenSelect:
		queryParenSelect, ok := query.(*sqlparser.ParenSelect)
		if !ok {
			return false
		}
		if !areEqualSelectStatement(queryParenSelect.Select, pattern.(*sqlparser.ParenSelect).Select) {
			return false
		}
	default:
		// unexpected
		return false
	}
	return true
}
func areEqualColName(query *sqlparser.ColName, pattern *sqlparser.ColName) bool {
	if !areEqualColIdent(query.Name, pattern.Name) {
		return false
	}
	if !areEqualTableName(query.Qualifier, pattern.Qualifier) {
		return false
	}
	if !reflect.DeepEqual(query.Metadata, pattern.Metadata) {
		return false
	}
	return true
}
func areEqualUpdateExpr(query, pattern *sqlparser.UpdateExpr) bool {
	if !areEqualExpr(query.Expr, pattern.Expr) {
		return false
	}
	if !areEqualColName(query.Name, pattern.Name) {
		return false
	}
	return true
}
func areEqualSubquery(query, pattern *sqlparser.Subquery) bool {
	if !areEqualSelectStatement(query.Select, pattern.Select) {
		return isSubqueryPattern(pattern)
	}
	return true
}
func areEqualValTuple(query sqlparser.ValTuple, pattern sqlparser.ValTuple) bool {
	if query == nil && pattern == nil {
		return true
	}
	if query == nil || pattern == nil {
		return false
	}
	for index := range pattern {
		if index >= len(query) {
			return false
		}
		if !areEqualExpr(query[index], pattern[index]) {
			return false
		}
	}

	// Case when %%LIST_OF_VALUES%% pattern presents in tuple of patterns.
	// It's allowed to use this pattern combined with %%VALUE%% only
	// at last position in tuple
	if len(query) > len(pattern) {
		patternValue, ok := pattern[len(pattern)-1].(*sqlparser.SQLVal)
		if !ok {
			return false
		}
		if !isListOfValuesPattern(patternValue) {
			return false
		}
	}
	return true
}

// Patterns detectors
func isListOfValuesPattern(pattern *sqlparser.SQLVal) bool {
	if pattern.Type != ListOfValuePatternStatement.Type {
		return false
	}

	return bytes.Equal(pattern.Val, ListOfValuePatternStatement.Val)
}
func isValuePattern(pattern *sqlparser.SQLVal) bool {
	if pattern.Type != ValuePatternStatement.Type {
		return false
	}
	return bytes.Equal(pattern.Val, ValuePatternStatement.Val)
}
func isColumnPattern(pattern sqlparser.ColIdent) bool {
	if pattern.Equal(ColumnPatternStatement) {
		return true
	}
	return false
}
func isSubqueryPattern(pattern *sqlparser.Subquery) bool {
	return reflect.DeepEqual(pattern.Select, SubqueryPatternStatement.(*sqlparser.Select))
}
func isWherePattern(pattern *sqlparser.Where) bool {
	if !strings.EqualFold(pattern.Type, WherePatternStatement.(*sqlparser.Select).Where.Type) {
		return false
	}
	if !areEqualExpr(pattern.Expr, WherePatternStatement.(*sqlparser.Select).Where.Expr) {
		return false
	}
	return true
}
func isStarExpr(pattern sqlparser.SelectExprs) bool {
	if len(pattern) != 1 {
		return false
	}
	if _, ok := pattern[0].(*sqlparser.StarExpr); !ok {
		return false
	}
	return true
}
