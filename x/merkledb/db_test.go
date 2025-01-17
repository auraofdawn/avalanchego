// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package merkledb

import (
	"bytes"
	"context"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/database/memdb"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/trace"
	"github.com/ava-labs/avalanchego/utils/hashing"
	"github.com/ava-labs/avalanchego/utils/maybe"
	"github.com/ava-labs/avalanchego/utils/units"
)

const defaultHistoryLength = 300

// newDB returns a new merkle database with the underlying type so that tests can access unexported fields
func newDB(ctx context.Context, db database.Database, config Config) (*merkleDB, error) {
	db, err := New(ctx, db, config)
	if err != nil {
		return nil, err
	}
	return db.(*merkleDB), nil
}

func newNoopTracer() trace.Tracer {
	tracer, _ := trace.New(trace.Config{Enabled: false})
	return tracer
}

func newDefaultConfig() Config {
	return Config{
		EvictionBatchSize: 100,
		HistoryLength:     defaultHistoryLength,
		NodeCacheSize:     1_000,
		Reg:               prometheus.NewRegistry(),
		Tracer:            newNoopTracer(),
	}
}

func Test_MerkleDB_Get_Safety(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)
	require.NoError(db.Put([]byte{0}, []byte{0, 1, 2}))

	val, err := db.Get([]byte{0})
	require.NoError(err)
	n, err := db.getNode(newPath([]byte{0}))
	require.NoError(err)
	val[0] = 1

	// node's value shouldn't be affected by the edit
	require.NotEqual(val, n.value.Value())
}

func Test_MerkleDB_GetValues_Safety(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)
	require.NoError(db.Put([]byte{0}, []byte{0, 1, 2}))

	vals, errs := db.GetValues(context.Background(), [][]byte{{0}})
	require.Len(errs, 1)
	require.NoError(errs[0])
	require.Equal([]byte{0, 1, 2}, vals[0])
	vals[0][0] = 1

	// editing the value array shouldn't affect the db
	vals, errs = db.GetValues(context.Background(), [][]byte{{0}})
	require.Len(errs, 1)
	require.NoError(errs[0])
	require.Equal([]byte{0, 1, 2}, vals[0])
}

func Test_MerkleDB_DB_Interface(t *testing.T) {
	for _, test := range database.Tests {
		db, err := getBasicDB()
		require.NoError(t, err)
		test(t, db)
	}
}

func Benchmark_MerkleDB_DBInterface(b *testing.B) {
	for _, size := range database.BenchmarkSizes {
		keys, values := database.SetupBenchmark(b, size[0], size[1], size[2])
		for _, bench := range database.Benchmarks {
			db, err := getBasicDB()
			require.NoError(b, err)
			bench(b, db, "merkledb", keys, values)
		}
	}
}

func Test_MerkleDB_DB_Load_Root_From_DB(t *testing.T) {
	require := require.New(t)
	rdb := memdb.New()
	defer rdb.Close()

	db, err := New(
		context.Background(),
		rdb,
		newDefaultConfig(),
	)
	require.NoError(err)

	// Populate initial set of keys
	keyCount := 100
	ops := make([]database.BatchOp, 0, keyCount)
	require.NoError(err)
	for i := 0; i < keyCount; i++ {
		k := []byte(strconv.Itoa(i))
		ops = append(ops, database.BatchOp{Key: k, Value: hashing.ComputeHash256(k)})
	}
	view, err := db.NewView(context.Background(), ops)
	require.NoError(err)
	require.NoError(view.CommitToDB(context.Background()))

	root, err := db.GetMerkleRoot(context.Background())
	require.NoError(err)

	require.NoError(db.Close())

	// reloading the DB, should set the root back to the one that was saved to the memdb
	db, err = New(
		context.Background(),
		rdb,
		newDefaultConfig(),
	)
	require.NoError(err)
	reloadedRoot, err := db.GetMerkleRoot(context.Background())
	require.NoError(err)
	require.Equal(root, reloadedRoot)
}

func Test_MerkleDB_DB_Rebuild(t *testing.T) {
	require := require.New(t)

	rdb := memdb.New()
	defer rdb.Close()

	initialSize := 10_000

	config := newDefaultConfig()
	config.NodeCacheSize = initialSize

	db, err := newDB(
		context.Background(),
		rdb,
		config,
	)
	require.NoError(err)

	// Populate initial set of keys
	ops := make([]database.BatchOp, 0, initialSize)
	require.NoError(err)
	for i := 0; i < initialSize; i++ {
		k := []byte(strconv.Itoa(i))
		ops = append(ops, database.BatchOp{Key: k, Value: hashing.ComputeHash256(k)})
	}
	view, err := db.NewView(context.Background(), ops)
	require.NoError(err)
	require.NoError(view.CommitToDB(context.Background()))

	root, err := db.GetMerkleRoot(context.Background())
	require.NoError(err)

	require.NoError(db.rebuild(context.Background()))

	rebuiltRoot, err := db.GetMerkleRoot(context.Background())
	require.NoError(err)
	require.Equal(root, rebuiltRoot)
}

func Test_MerkleDB_Failed_Batch_Commit(t *testing.T) {
	require := require.New(t)

	memDB := memdb.New()
	db, err := New(
		context.Background(),
		memDB,
		newDefaultConfig(),
	)
	require.NoError(err)

	_ = memDB.Close()

	batch := db.NewBatch()
	require.NoError(batch.Put([]byte("key1"), []byte("1")))
	require.NoError(batch.Put([]byte("key2"), []byte("2")))
	require.NoError(batch.Put([]byte("key3"), []byte("3")))
	err = batch.Write()
	require.ErrorIs(err, database.ErrClosed)
}

func Test_MerkleDB_Value_Cache(t *testing.T) {
	require := require.New(t)

	memDB := memdb.New()
	db, err := New(
		context.Background(),
		memDB,
		newDefaultConfig(),
	)
	require.NoError(err)

	batch := db.NewBatch()
	require.NoError(batch.Put([]byte("key1"), []byte("1")))
	require.NoError(batch.Put([]byte("key2"), []byte("2")))
	require.NoError(batch.Write())

	batch = db.NewBatch()
	// force key2 to be inserted into the cache as not found
	require.NoError(batch.Delete([]byte("key2")))
	require.NoError(batch.Write())

	require.NoError(memDB.Close())

	// still works because key1 is read from cache
	value, err := db.Get([]byte("key1"))
	require.NoError(err)
	require.Equal([]byte("1"), value)

	// still returns missing instead of closed because key2 is read from cache
	_, err = db.Get([]byte("key2"))
	require.ErrorIs(err, database.ErrNotFound)
}

func Test_MerkleDB_Invalidate_Siblings_On_Commit(t *testing.T) {
	require := require.New(t)

	dbTrie, err := getBasicDB()
	require.NoError(err)
	require.NotNil(dbTrie)

	viewToCommit, err := dbTrie.NewView(context.Background(), []database.BatchOp{{Key: []byte{0}, Value: []byte{0}}})
	require.NoError(err)

	sibling1, err := dbTrie.NewView(context.Background(), nil)
	require.NoError(err)
	sibling2, err := dbTrie.NewView(context.Background(), nil)
	require.NoError(err)

	require.False(sibling1.(*trieView).isInvalid())
	require.False(sibling2.(*trieView).isInvalid())

	require.NoError(viewToCommit.CommitToDB(context.Background()))

	require.True(sibling1.(*trieView).isInvalid())
	require.True(sibling2.(*trieView).isInvalid())
	require.False(viewToCommit.(*trieView).isInvalid())
}

func Test_MerkleDB_Commit_Proof_To_Empty_Trie(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)
	batch := db.NewBatch()
	require.NoError(batch.Put([]byte("key1"), []byte("1")))
	require.NoError(batch.Put([]byte("key2"), []byte("2")))
	require.NoError(batch.Put([]byte("key3"), []byte("3")))
	require.NoError(batch.Write())

	proof, err := db.GetRangeProof(context.Background(), maybe.Some([]byte("key1")), maybe.Some([]byte("key3")), 10)
	require.NoError(err)

	freshDB, err := getBasicDB()
	require.NoError(err)

	require.NoError(freshDB.CommitRangeProof(context.Background(), maybe.Some([]byte("key1")), proof))

	value, err := freshDB.Get([]byte("key2"))
	require.NoError(err)
	require.Equal([]byte("2"), value)

	freshRoot, err := freshDB.GetMerkleRoot(context.Background())
	require.NoError(err)
	oldRoot, err := db.GetMerkleRoot(context.Background())
	require.NoError(err)
	require.Equal(oldRoot, freshRoot)
}

func Test_MerkleDB_Commit_Proof_To_Filled_Trie(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)
	batch := db.NewBatch()
	require.NoError(batch.Put([]byte("key1"), []byte("1")))
	require.NoError(batch.Put([]byte("key2"), []byte("2")))
	require.NoError(batch.Put([]byte("key3"), []byte("3")))
	require.NoError(batch.Write())

	proof, err := db.GetRangeProof(context.Background(), maybe.Some([]byte("key1")), maybe.Some([]byte("key3")), 10)
	require.NoError(err)

	freshDB, err := getBasicDB()
	require.NoError(err)
	batch = freshDB.NewBatch()
	require.NoError(batch.Put([]byte("key1"), []byte("3")))
	require.NoError(batch.Put([]byte("key2"), []byte("4")))
	require.NoError(batch.Put([]byte("key3"), []byte("5")))
	require.NoError(batch.Put([]byte("key25"), []byte("5")))
	require.NoError(batch.Write())

	require.NoError(freshDB.CommitRangeProof(context.Background(), maybe.Some([]byte("key1")), proof))

	value, err := freshDB.Get([]byte("key2"))
	require.NoError(err)
	require.Equal([]byte("2"), value)

	freshRoot, err := freshDB.GetMerkleRoot(context.Background())
	require.NoError(err)
	oldRoot, err := db.GetMerkleRoot(context.Background())
	require.NoError(err)
	require.Equal(oldRoot, freshRoot)
}

func Test_MerkleDB_GetValues(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)

	writeBasicBatch(t, db)
	keys := [][]byte{{0}, {1}, {2}, {10}}
	values, errors := db.GetValues(context.Background(), keys)
	require.Len(values, len(keys))
	require.Len(errors, len(keys))

	// first 3 have values
	// last was not found
	require.NoError(errors[0])
	require.NoError(errors[1])
	require.NoError(errors[2])
	require.ErrorIs(errors[3], database.ErrNotFound)

	require.Equal([]byte{0}, values[0])
	require.Equal([]byte{1}, values[1])
	require.Equal([]byte{2}, values[2])
	require.Nil(values[3])
}

func Test_MerkleDB_InsertNil(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)
	batch := db.NewBatch()
	require.NoError(batch.Put([]byte("key0"), nil))
	require.NoError(batch.Write())

	value, err := db.Get([]byte("key0"))
	require.NoError(err)
	require.Nil(value)

	value, err = getNodeValue(db, "key0")
	require.NoError(err)
	require.Nil(value)
}

func Test_MerkleDB_InsertAndRetrieve(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)

	// value hasn't been inserted so shouldn't exist
	value, err := db.Get([]byte("key"))
	require.ErrorIs(err, database.ErrNotFound)
	require.Nil(value)

	require.NoError(db.Put([]byte("key"), []byte("value")))

	value, err = db.Get([]byte("key"))
	require.NoError(err)
	require.Equal([]byte("value"), value)
}

func Test_MerkleDB_HealthCheck(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)
	val, err := db.HealthCheck(context.Background())
	require.NoError(err)
	require.Nil(val)
}

func Test_MerkleDB_Overwrite(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)

	require.NoError(db.Put([]byte("key"), []byte("value0")))

	value, err := db.Get([]byte("key"))
	require.NoError(err)
	require.Equal([]byte("value0"), value)

	require.NoError(db.Put([]byte("key"), []byte("value1")))

	value, err = db.Get([]byte("key"))
	require.NoError(err)
	require.Equal([]byte("value1"), value)
}

func Test_MerkleDB_Delete(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)

	require.NoError(db.Put([]byte("key"), []byte("value0")))

	value, err := db.Get([]byte("key"))
	require.NoError(err)
	require.Equal([]byte("value0"), value)

	require.NoError(db.Delete([]byte("key")))

	value, err = db.Get([]byte("key"))
	require.ErrorIs(err, database.ErrNotFound)
	require.Nil(value)
}

func Test_MerkleDB_DeleteMissingKey(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)

	require.NoError(db.Delete([]byte("key")))
}

// Test that untracked views aren't persisted to [db.childViews].
func TestDatabaseNewUntrackedView(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)

	// Create a new untracked view.
	view, err := db.newUntrackedView([]database.BatchOp{{Key: []byte{1}, Value: []byte{1}}})
	require.NoError(err)
	require.Empty(db.childViews)

	// Commit the view
	require.NoError(view.CommitToDB(context.Background()))

	// The untracked view should not be tracked by the parent database.
	require.Empty(db.childViews)
}

// Test that tracked views are persisted to [db.childViews].
func TestDatabaseNewViewTracked(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)

	// Create a new tracked view.
	view, err := db.NewView(context.Background(), []database.BatchOp{{Key: []byte{1}, Value: []byte{1}}})
	require.NoError(err)
	require.Len(db.childViews, 1)

	// Commit the view
	require.NoError(view.CommitToDB(context.Background()))

	// The untracked view should be tracked by the parent database.
	require.Contains(db.childViews, view)
	require.Len(db.childViews, 1)
}

func TestDatabaseCommitChanges(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)
	dbRoot := db.getMerkleRoot()

	// Committing a nil view should be a no-op.
	require.NoError(db.CommitToDB(context.Background()))
	require.Equal(dbRoot, db.getMerkleRoot()) // Root didn't change

	// Committing an invalid view should fail.
	invalidView, err := db.NewView(context.Background(), nil)
	require.NoError(err)
	invalidView.(*trieView).invalidate()
	err = invalidView.CommitToDB(context.Background())
	require.ErrorIs(err, ErrInvalid)

	// Add key-value pairs to the database
	require.NoError(db.Put([]byte{1}, []byte{1}))
	require.NoError(db.Put([]byte{2}, []byte{2}))

	// Make a view and insert/delete a key-value pair.
	view1Intf, err := db.NewView(context.Background(), []database.BatchOp{
		{Key: []byte{3}, Value: []byte{3}},
		{Key: []byte{1}, Delete: true},
	})
	require.NoError(err)
	require.IsType(&trieView{}, view1Intf)
	view1 := view1Intf.(*trieView)
	view1Root, err := view1.GetMerkleRoot(context.Background())
	require.NoError(err)

	// Make a second view
	view2Intf, err := db.NewView(context.Background(), nil)
	require.NoError(err)
	require.IsType(&trieView{}, view2Intf)
	view2 := view2Intf.(*trieView)

	// Make a view atop a view
	view3Intf, err := view1.NewView(context.Background(), nil)
	require.NoError(err)
	require.IsType(&trieView{}, view3Intf)
	view3 := view3Intf.(*trieView)

	// view3
	//  |
	// view1   view2
	//     \  /
	//      db

	// Commit view1
	require.NoError(view1.commitToDB(context.Background()))

	// Make sure the key-value pairs are correct.
	_, err = db.Get([]byte{1})
	require.ErrorIs(err, database.ErrNotFound)
	value, err := db.Get([]byte{2})
	require.NoError(err)
	require.Equal([]byte{2}, value)
	value, err = db.Get([]byte{3})
	require.NoError(err)
	require.Equal([]byte{3}, value)

	// Make sure the root is right
	require.Equal(view1Root, db.getMerkleRoot())

	// Make sure view2 is invalid and view1 and view3 is valid.
	require.False(view1.invalidated)
	require.True(view2.invalidated)
	require.False(view3.invalidated)

	// Make sure view2 isn't tracked by the database.
	require.NotContains(db.childViews, view2)

	// Make sure view1 and view3 is tracked by the database.
	require.Contains(db.childViews, view1)
	require.Contains(db.childViews, view3)

	// Make sure view3 is now a child of db.
	require.Equal(db, view3.parentTrie)
}

func TestDatabaseInvalidateChildrenExcept(t *testing.T) {
	require := require.New(t)

	db, err := getBasicDB()
	require.NoError(err)

	// Create children
	view1Intf, err := db.NewView(context.Background(), nil)
	require.NoError(err)
	require.IsType(&trieView{}, view1Intf)
	view1 := view1Intf.(*trieView)

	view2Intf, err := db.NewView(context.Background(), nil)
	require.NoError(err)
	require.IsType(&trieView{}, view2Intf)
	view2 := view2Intf.(*trieView)

	view3Intf, err := db.NewView(context.Background(), nil)
	require.NoError(err)
	require.IsType(&trieView{}, view3Intf)
	view3 := view3Intf.(*trieView)

	db.invalidateChildrenExcept(view1)

	// Make sure view1 is valid and view2 and view3 are invalid.
	require.False(view1.invalidated)
	require.True(view2.invalidated)
	require.True(view3.invalidated)
	require.Contains(db.childViews, view1)
	require.Len(db.childViews, 1)

	db.invalidateChildrenExcept(nil)

	// Make sure all views are invalid.
	require.True(view1.invalidated)
	require.True(view2.invalidated)
	require.True(view3.invalidated)
	require.Empty(db.childViews)

	// Calling with an untracked view doesn't add the untracked view
	db.invalidateChildrenExcept(view1)
	require.Empty(db.childViews)
}

func Test_MerkleDB_Random_Insert_Ordering(t *testing.T) {
	require := require.New(t)

	totalState := 1000
	var (
		allKeys [][]byte
		keyMap  map[string]struct{}
	)
	genKey := func(r *rand.Rand) []byte {
		count := 0
		for {
			var key []byte
			if len(allKeys) > 2 && r.Intn(100) < 10 {
				// new prefixed key
				prefix := allKeys[r.Intn(len(allKeys))]
				key = make([]byte, r.Intn(50)+len(prefix))
				copy(key, prefix)
				_, err := r.Read(key[len(prefix):])
				require.NoError(err)
			} else {
				key = make([]byte, r.Intn(50))
				_, err := r.Read(key)
				require.NoError(err)
			}
			if _, ok := keyMap[string(key)]; !ok {
				allKeys = append(allKeys, key)
				keyMap[string(key)] = struct{}{}
				return key
			}
			count++
		}
	}

	for i := 0; i < 3; i++ {
		now := time.Now().UnixNano()
		t.Logf("seed for iter %d: %d", i, now)
		r := rand.New(rand.NewSource(now)) // #nosec G404

		ops := make([]database.BatchOp, 0, totalState)
		allKeys = [][]byte{}
		keyMap = map[string]struct{}{}
		for x := 0; x < totalState; x++ {
			key := genKey(r)
			value := make([]byte, r.Intn(51))
			if len(value) == 51 {
				value = nil
			} else {
				_, err := r.Read(value)
				require.NoError(err)
			}
			ops = append(ops, database.BatchOp{Key: key, Value: value})
		}
		db, err := getBasicDB()
		require.NoError(err)
		result, err := db.NewView(context.Background(), ops)
		require.NoError(err)
		primaryRoot, err := result.GetMerkleRoot(context.Background())
		require.NoError(err)
		for shuffleIndex := 0; shuffleIndex < 3; shuffleIndex++ {
			r.Shuffle(totalState, func(i, j int) {
				ops[i], ops[j] = ops[j], ops[i]
			})
			result, err := db.NewView(context.Background(), ops)
			require.NoError(err)
			newRoot, err := result.GetMerkleRoot(context.Background())
			require.NoError(err)
			require.Equal(primaryRoot, newRoot)
		}
	}
}

func Test_MerkleDB_RandomCases(t *testing.T) {
	require := require.New(t)

	for i := 150; i < 500; i += 10 {
		now := time.Now().UnixNano()
		t.Logf("seed for iter %d: %d", i, now)
		r := rand.New(rand.NewSource(now)) // #nosec G404
		runRandDBTest(require, r, generate(require, r, i, .01))
	}
}

func Test_MerkleDB_RandomCases_InitialValues(t *testing.T) {
	require := require.New(t)

	now := time.Now().UnixNano()
	t.Logf("seed: %d", now)
	r := rand.New(rand.NewSource(now)) // #nosec G404
	runRandDBTest(require, r, generateInitialValues(require, r, 1000, 2500, 0.0))
}

// randTest performs random trie operations.
// Instances of this test are created by Generate.
type randTest []randTestStep

type randTestStep struct {
	op    int
	key   []byte // for opUpdate, opDelete, opGet
	value []byte // for opUpdate
}

const (
	opUpdate = iota
	opDelete
	opGet
	opWriteBatch
	opGenerateRangeProof
	opGenerateChangeProof
	opCheckhash
	opMax // boundary value, not an actual op
)

func runRandDBTest(require *require.Assertions, r *rand.Rand, rt randTest) {
	db, err := getBasicDB()
	require.NoError(err)

	startRoot, err := db.GetMerkleRoot(context.Background())
	require.NoError(err)

	values := make(map[path][]byte) // tracks content of the trie
	currentBatch := db.NewBatch()
	currentValues := make(map[path][]byte)
	deleteValues := make(map[path]struct{})
	pastRoots := []ids.ID{}

	for i, step := range rt {
		require.LessOrEqual(i, len(rt))
		switch step.op {
		case opUpdate:
			require.NoError(currentBatch.Put(step.key, step.value))
			currentValues[newPath(step.key)] = step.value
			delete(deleteValues, newPath(step.key))
		case opDelete:
			require.NoError(currentBatch.Delete(step.key))
			deleteValues[newPath(step.key)] = struct{}{}
			delete(currentValues, newPath(step.key))
		case opGenerateRangeProof:
			root, err := db.GetMerkleRoot(context.Background())
			require.NoError(err)
			if len(pastRoots) > 0 {
				root = pastRoots[r.Intn(len(pastRoots))]
			}
			start := maybe.Nothing[[]byte]()
			if len(step.key) > 0 {
				start = maybe.Some(step.key)
			}
			end := maybe.Nothing[[]byte]()
			if len(step.value) > 0 {
				end = maybe.Some(step.value)
			}

			rangeProof, err := db.GetRangeProofAtRoot(context.Background(), root, start, end, 100)
			require.NoError(err)
			require.NoError(rangeProof.Verify(
				context.Background(),
				start,
				end,
				root,
			))
			require.LessOrEqual(len(rangeProof.KeyValues), 100)
		case opGenerateChangeProof:
			root, err := db.GetMerkleRoot(context.Background())
			require.NoError(err)
			if len(pastRoots) > 1 {
				root = pastRoots[r.Intn(len(pastRoots))]
			}
			end := maybe.Nothing[[]byte]()
			if len(step.value) > 0 {
				end = maybe.Some(step.value)
			}
			start := maybe.Nothing[[]byte]()
			if len(step.key) > 0 {
				start = maybe.Some(step.key)
			}

			changeProof, err := db.GetChangeProof(context.Background(), startRoot, root, start, end, 100)
			if startRoot == root {
				require.ErrorIs(err, errSameRoot)
				continue
			}
			require.NoError(err)
			changeProofDB, err := getBasicDB()
			require.NoError(err)

			require.NoError(changeProofDB.VerifyChangeProof(
				context.Background(),
				changeProof,
				start,
				end,
				root,
			))
			require.LessOrEqual(len(changeProof.KeyChanges), 100)
		case opWriteBatch:
			oldRoot, err := db.GetMerkleRoot(context.Background())
			require.NoError(err)
			require.NoError(currentBatch.Write())
			for key, value := range currentValues {
				values[key] = value
			}
			for key := range deleteValues {
				delete(values, key)
			}

			if len(currentValues) == 0 && len(deleteValues) == 0 {
				continue
			}
			newRoot, err := db.GetMerkleRoot(context.Background())
			require.NoError(err)
			if oldRoot != newRoot {
				pastRoots = append(pastRoots, newRoot)
				if len(pastRoots) > 300 {
					pastRoots = pastRoots[len(pastRoots)-300:]
				}
			}
			currentValues = map[path][]byte{}
			deleteValues = map[path]struct{}{}
			currentBatch = db.NewBatch()
		case opGet:
			v, err := db.Get(step.key)
			if err != nil {
				require.ErrorIs(err, database.ErrNotFound)
			}
			want := values[newPath(step.key)]
			require.True(bytes.Equal(want, v)) // Use bytes.Equal so nil treated equal to []byte{}
			trieValue, err := getNodeValue(db, string(step.key))
			if err != nil {
				require.ErrorIs(err, database.ErrNotFound)
			}
			require.True(bytes.Equal(want, trieValue)) // Use bytes.Equal so nil treated equal to []byte{}
		case opCheckhash:
			dbTrie, err := newDatabase(
				context.Background(),
				memdb.New(),
				newDefaultConfig(),
				&mockMetrics{},
			)
			require.NoError(err)
			ops := make([]database.BatchOp, 0, len(values))
			for key, value := range values {
				ops = append(ops, database.BatchOp{Key: key.Serialize().Value, Value: value})
			}
			newView, err := dbTrie.NewView(context.Background(), ops)
			require.NoError(err)

			calculatedRoot, err := newView.GetMerkleRoot(context.Background())
			require.NoError(err)
			dbRoot, err := db.GetMerkleRoot(context.Background())
			require.NoError(err)
			require.Equal(dbRoot, calculatedRoot)
		}
	}
}

func generateWithKeys(require *require.Assertions, allKeys [][]byte, r *rand.Rand, size int, percentChanceToFullHash float64) randTest {
	genKey := func() []byte {
		if len(allKeys) < 2 || r.Intn(100) < 10 {
			// new key
			key := make([]byte, r.Intn(50))
			_, err := r.Read(key)
			require.NoError(err)
			allKeys = append(allKeys, key)
			return key
		}
		if len(allKeys) > 2 && r.Intn(100) < 10 {
			// new prefixed key
			prefix := allKeys[r.Intn(len(allKeys))]
			key := make([]byte, r.Intn(50)+len(prefix))
			copy(key, prefix)
			_, err := r.Read(key[len(prefix):])
			require.NoError(err)
			allKeys = append(allKeys, key)
			return key
		}
		// use existing key
		return allKeys[r.Intn(len(allKeys))]
	}

	genEnd := func(key []byte) []byte {
		shouldBeNil := r.Intn(10)
		if shouldBeNil == 0 {
			return nil
		}

		endKey := make([]byte, len(key))
		copy(endKey, key)
		for i := 0; i < len(endKey); i += 2 {
			n := r.Intn(len(endKey))
			if endKey[n] < 250 {
				endKey[n] += byte(r.Intn(int(255 - endKey[n])))
			}
		}
		return endKey
	}

	var steps randTest
	for i := 0; i < size-1; {
		step := randTestStep{op: r.Intn(opMax)}
		switch step.op {
		case opUpdate:
			step.key = genKey()
			step.value = make([]byte, r.Intn(50))
			if len(step.value) == 51 {
				step.value = nil
			} else {
				_, err := r.Read(step.value)
				require.NoError(err)
			}
		case opGet, opDelete:
			step.key = genKey()
		case opGenerateRangeProof, opGenerateChangeProof:
			step.key = genKey()
			step.value = genEnd(step.key)
		case opCheckhash:
			// this gets really expensive so control how often it happens
			if r.Float64() >= percentChanceToFullHash {
				continue
			}
		}
		steps = append(steps, step)
		i++
	}
	// always end with a full hash of the trie
	steps = append(steps, randTestStep{op: opCheckhash})
	return steps
}

func generateInitialValues(require *require.Assertions, r *rand.Rand, initialValues int, size int, percentChanceToFullHash float64) randTest {
	var allKeys [][]byte
	genKey := func() []byte {
		// new prefixed key
		if len(allKeys) > 2 && r.Intn(100) < 10 {
			prefix := allKeys[r.Intn(len(allKeys))]
			key := make([]byte, r.Intn(50)+len(prefix))
			copy(key, prefix)
			_, err := r.Read(key[len(prefix):])
			require.NoError(err)
			allKeys = append(allKeys, key)
			return key
		}

		// new key
		key := make([]byte, r.Intn(50))
		_, err := r.Read(key)
		require.NoError(err)
		allKeys = append(allKeys, key)
		return key
	}

	var steps randTest
	for i := 0; i < initialValues; i++ {
		step := randTestStep{op: opUpdate}
		step.key = genKey()
		step.value = make([]byte, r.Intn(51))
		if len(step.value) == 51 {
			step.value = nil
		} else {
			_, err := r.Read(step.value)
			require.NoError(err)
		}
		steps = append(steps, step)
	}
	steps = append(steps, randTestStep{op: opWriteBatch})
	steps = append(steps, generateWithKeys(require, allKeys, r, size, percentChanceToFullHash)...)
	return steps
}

func generate(require *require.Assertions, r *rand.Rand, size int, percentChanceToFullHash float64) randTest {
	var allKeys [][]byte
	return generateWithKeys(require, allKeys, r, size, percentChanceToFullHash)
}

// Inserts [n] random key/value pairs into each database.
// Deletes [deletePortion] of the key/value pairs after insertion.
func insertRandomKeyValues(
	require *require.Assertions,
	rand *rand.Rand,
	dbs []database.Database,
	numKeyValues int,
	deletePortion float64,
) {
	maxKeyLen := units.KiB
	maxValLen := 4 * units.KiB

	require.GreaterOrEqual(deletePortion, float64(0))
	require.LessOrEqual(deletePortion, float64(1))
	for i := 0; i < numKeyValues; i++ {
		keyLen := rand.Intn(maxKeyLen)
		key := make([]byte, keyLen)
		_, _ = rand.Read(key)

		valueLen := rand.Intn(maxValLen)
		value := make([]byte, valueLen)
		_, _ = rand.Read(value)
		for _, db := range dbs {
			require.NoError(db.Put(key, value))
		}

		if rand.Float64() < deletePortion {
			for _, db := range dbs {
				require.NoError(db.Delete(key))
			}
		}
	}
}
