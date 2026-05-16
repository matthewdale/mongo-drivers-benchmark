package conformance

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestConformance_InsertOne_ThenFind(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	id := NewObjectID()
	doc := Doc(fmt.Sprintf(`{"_id": %s, "name": "Alice"}`, string(id)))

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			InsertOne("users", doc),
			Find("users", JSON(`{}`)).WithSort(JSON(`{"_id": 1}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	if got := len(resp.Results); got != 2 {
		t.Fatalf("results=%d, want 2", got)
	}
	ExpectOK(t, resp.Results[0], "insertOne")
	insert, err := DecodeData[InsertOneData](resp.Results[0])
	if err != nil {
		t.Fatalf("decode insert: %v", err)
	}
	if ExtJSONOID(insert.InsertedID) != ExtJSONOID(id) {
		t.Errorf("inserted_id = %s, want %s", string(insert.InsertedID), string(id))
	}

	ExpectOK(t, resp.Results[1], "find")
	find, err := DecodeData[FindData](resp.Results[1])
	if err != nil {
		t.Fatalf("decode find: %v", err)
	}
	if find.Count != 1 || len(find.Documents) != 1 {
		t.Fatalf("find count/len = %d/%d, want 1/1", find.Count, len(find.Documents))
	}
}

func TestConformance_InsertMany_ThenCount(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	var docs []Doc
	for i := 0; i < 25; i++ {
		docs = append(docs, JSON(fmt.Sprintf(`{"n": %d}`, i)))
	}

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			InsertMany("items", docs),
			CountDocuments("items", JSON(`{}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "insertMany")
	insert, err := DecodeData[InsertManyData](resp.Results[0])
	if err != nil {
		t.Fatalf("decode insertMany: %v", err)
	}
	if insert.InsertedCount != 25 {
		t.Errorf("inserted_count = %d, want 25", insert.InsertedCount)
	}
	if len(insert.InsertedIDs) != 25 {
		t.Errorf("inserted_ids len = %d, want 25", len(insert.InsertedIDs))
	}

	ExpectOK(t, resp.Results[1], "countDocuments")
	cnt, _ := DecodeData[CountData](resp.Results[1])
	if cnt.Count != 25 {
		t.Errorf("count = %d, want 25", cnt.Count)
	}
}

func TestConformance_Find_WithSortLimitSkip(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	var docs []Doc
	for i := 0; i < 10; i++ {
		docs = append(docs, JSON(fmt.Sprintf(`{"n": %d}`, i)))
	}
	_, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops:      []Op{InsertMany("nums", docs)},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			Find("nums", JSON(`{}`)).WithSort(JSON(`{"n": 1}`)).WithSkip(3).WithLimit(4),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "find")
	find, _ := DecodeData[FindData](resp.Results[0])
	if find.Count != 4 || len(find.Documents) != 4 {
		t.Fatalf("find count/len = %d/%d, want 4/4", find.Count, len(find.Documents))
	}
	// Expect n=3,4,5,6 in order.
	for i, want := range []int{3, 4, 5, 6} {
		var d struct{ N int }
		if err := json.Unmarshal(find.Documents[i], &d); err != nil {
			t.Fatalf("decode doc %d: %v (raw=%s)", i, err, string(find.Documents[i]))
		}
		if d.N != want {
			t.Errorf("documents[%d].n = %d, want %d", i, d.N, want)
		}
	}
}

func TestConformance_Find_WithProjection(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	id := NewObjectID()
	doc := Doc(fmt.Sprintf(`{"_id": %s, "a": 1, "b": 2, "c": 3}`, string(id)))
	if _, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops:      []Op{InsertOne("things", doc)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			Find("things", JSON(`{}`)).
				WithProjection(JSON(`{"a": 1, "_id": 0}`)).
				WithSort(JSON(`{"a": 1}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "find")
	find, _ := DecodeData[FindData](resp.Results[0])
	if len(find.Documents) != 1 {
		t.Fatalf("documents=%d, want 1", len(find.Documents))
	}
	var got map[string]any
	if err := json.Unmarshal(find.Documents[0], &got); err != nil {
		t.Fatalf("decode projection: %v", err)
	}
	if _, ok := got["b"]; ok {
		t.Errorf("projection leaked field b: %v", got)
	}
	if _, ok := got["c"]; ok {
		t.Errorf("projection leaked field c: %v", got)
	}
	if _, ok := got["_id"]; ok {
		t.Errorf("projection leaked field _id: %v", got)
	}
	if _, ok := got["a"]; !ok {
		t.Errorf("projection omitted field a: %v", got)
	}
}

func TestConformance_UpdateOne_Inc(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	id := NewObjectID()
	doc := Doc(fmt.Sprintf(`{"_id": %s, "n": 1}`, string(id)))

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			InsertOne("counters", doc),
			UpdateOne("counters", JSON(`{"n": 1}`), JSON(`{"$inc": {"n": 1}}`)),
			Find("counters", JSON(`{}`)).WithSort(JSON(`{"_id": 1}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[1], "updateOne")
	upd, _ := DecodeData[UpdateData](resp.Results[1])
	if upd.MatchedCount != 1 || upd.ModifiedCount != 1 {
		t.Errorf("update matched/modified = %d/%d, want 1/1", upd.MatchedCount, upd.ModifiedCount)
	}
	ExpectOK(t, resp.Results[2], "find")
	find, _ := DecodeData[FindData](resp.Results[2])
	if len(find.Documents) != 1 {
		t.Fatalf("documents=%d", len(find.Documents))
	}
	var d struct{ N int }
	_ = json.Unmarshal(find.Documents[0], &d)
	if d.N != 2 {
		t.Errorf("n = %d after $inc, want 2", d.N)
	}
}

func TestConformance_UpdateMany_MatchedAndModifiedCounts(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	var docs []Doc
	for i := 0; i < 6; i++ {
		// Three with tag=A, three with tag=B.
		tag := "A"
		if i%2 == 1 {
			tag = "B"
		}
		docs = append(docs, JSON(fmt.Sprintf(`{"i": %d, "tag": %q}`, i, tag)))
	}
	if _, _, err := c.Ops(Ctx(t), OpsRequest{Database: db, Ops: []Op{InsertMany("things", docs)}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			UpdateMany("things", JSON(`{"tag": "A"}`), JSON(`{"$set": {"updated": true}}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "updateMany")
	upd, _ := DecodeData[UpdateData](resp.Results[0])
	if upd.MatchedCount != 3 {
		t.Errorf("matched_count = %d, want 3", upd.MatchedCount)
	}
	if upd.ModifiedCount != 3 {
		t.Errorf("modified_count = %d, want 3", upd.ModifiedCount)
	}
}

func TestConformance_UpdateOne_Upsert(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			UpdateOne("things", JSON(`{"key": "new"}`), JSON(`{"$set": {"value": 42}}`)).WithUpsert(true),
			CountDocuments("things", JSON(`{}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "updateOne")
	upd, _ := DecodeData[UpdateData](resp.Results[0])
	if upd.MatchedCount != 0 {
		t.Errorf("matched_count = %d, want 0", upd.MatchedCount)
	}
	if len(upd.UpsertedID) == 0 {
		t.Errorf("upserted_id missing on upsert insert")
	}
	cnt, _ := DecodeData[CountData](resp.Results[1])
	if cnt.Count != 1 {
		t.Errorf("count = %d, want 1", cnt.Count)
	}
}

func TestConformance_ReplaceOne_ThenFind(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	id := NewObjectID()
	if _, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops:      []Op{InsertOne("things", Doc(fmt.Sprintf(`{"_id": %s, "v": 1}`, string(id))))},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			ReplaceOne("things", Doc(fmt.Sprintf(`{"_id": %s}`, string(id))), JSON(`{"v": 2, "extra": "ok"}`)),
			Find("things", JSON(`{}`)).WithSort(JSON(`{"_id": 1}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "replaceOne")
	upd, _ := DecodeData[UpdateData](resp.Results[0])
	if upd.MatchedCount != 1 || upd.ModifiedCount != 1 {
		t.Errorf("replace matched/modified = %d/%d", upd.MatchedCount, upd.ModifiedCount)
	}
	find, _ := DecodeData[FindData](resp.Results[1])
	if len(find.Documents) != 1 {
		t.Fatalf("documents=%d", len(find.Documents))
	}
	var d map[string]any
	_ = json.Unmarshal(find.Documents[0], &d)
	if d["v"].(float64) != 2 {
		t.Errorf("v = %v, want 2", d["v"])
	}
	if d["extra"] != "ok" {
		t.Errorf("extra = %v, want ok", d["extra"])
	}
}

func TestConformance_DeleteOne_DecrementsCount(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	var docs []Doc
	for i := 0; i < 5; i++ {
		docs = append(docs, JSON(fmt.Sprintf(`{"n": %d}`, i)))
	}
	if _, _, err := c.Ops(Ctx(t), OpsRequest{Database: db, Ops: []Op{InsertMany("nums", docs)}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			DeleteOne("nums", JSON(`{"n": 2}`)),
			CountDocuments("nums", JSON(`{}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "deleteOne")
	del, _ := DecodeData[DeleteData](resp.Results[0])
	if del.DeletedCount != 1 {
		t.Errorf("deleted_count = %d, want 1", del.DeletedCount)
	}
	cnt, _ := DecodeData[CountData](resp.Results[1])
	if cnt.Count != 4 {
		t.Errorf("count after delete = %d, want 4", cnt.Count)
	}
}

func TestConformance_DeleteMany_Filtered(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	var docs []Doc
	for i := 0; i < 6; i++ {
		tag := "A"
		if i%2 == 1 {
			tag = "B"
		}
		docs = append(docs, JSON(fmt.Sprintf(`{"i": %d, "tag": %q}`, i, tag)))
	}
	if _, _, err := c.Ops(Ctx(t), OpsRequest{Database: db, Ops: []Op{InsertMany("things", docs)}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			DeleteMany("things", JSON(`{"tag": "A"}`)),
			CountDocuments("things", JSON(`{}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "deleteMany")
	del, _ := DecodeData[DeleteData](resp.Results[0])
	if del.DeletedCount != 3 {
		t.Errorf("deleted_count = %d, want 3", del.DeletedCount)
	}
	cnt, _ := DecodeData[CountData](resp.Results[1])
	if cnt.Count != 3 {
		t.Errorf("remaining count = %d, want 3", cnt.Count)
	}
}

func TestConformance_Aggregate_MatchGroup(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	// 4 A's summing to 6, 3 B's summing to 9.
	docs := []Doc{
		JSON(`{"tag": "A", "v": 1}`),
		JSON(`{"tag": "A", "v": 2}`),
		JSON(`{"tag": "A", "v": 0}`),
		JSON(`{"tag": "A", "v": 3}`),
		JSON(`{"tag": "B", "v": 2}`),
		JSON(`{"tag": "B", "v": 3}`),
		JSON(`{"tag": "B", "v": 4}`),
	}
	if _, _, err := c.Ops(Ctx(t), OpsRequest{Database: db, Ops: []Op{InsertMany("things", docs)}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pipeline := []Doc{
		JSON(`{"$group": {"_id": "$tag", "sum": {"$sum": "$v"}}}`),
		JSON(`{"$sort": {"_id": 1}}`),
	}
	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops:      []Op{Aggregate("things", pipeline)},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "aggregate")
	agg, _ := DecodeData[FindData](resp.Results[0])
	if len(agg.Documents) != 2 {
		t.Fatalf("agg docs = %d, want 2", len(agg.Documents))
	}
	type row struct {
		ID  string  `json:"_id"`
		Sum float64 `json:"sum"`
	}
	var first, second row
	_ = json.Unmarshal(agg.Documents[0], &first)
	_ = json.Unmarshal(agg.Documents[1], &second)
	if first.ID != "A" || first.Sum != 6 {
		t.Errorf("first row = %+v, want {A 6}", first)
	}
	if second.ID != "B" || second.Sum != 9 {
		t.Errorf("second row = %+v, want {B 9}", second)
	}
}

func TestConformance_BulkWrite_Mixed(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	id := NewObjectID()
	// Seed one doc so updateOne has something to hit.
	if _, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops:      []Op{InsertOne("things", Doc(fmt.Sprintf(`{"_id": %s, "v": 1}`, string(id))))},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			BulkWrite("things", []Op{
				{Name: "insertOne", Document: JSON(`{"v": 10}`)},
				{Name: "insertOne", Document: JSON(`{"v": 11}`)},
				{Name: "updateOne", Filter: Doc(fmt.Sprintf(`{"_id": %s}`, string(id))), Update: JSON(`{"$set": {"v": 99}}`)},
				{Name: "deleteOne", Filter: JSON(`{"v": 10}`)},
			}),
			CountDocuments("things", JSON(`{}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "bulkWrite")
	bw, err := DecodeData[BulkWriteData](resp.Results[0])
	if err != nil {
		t.Fatalf("decode bulkWrite: %v", err)
	}
	if bw.InsertedCount != 2 {
		t.Errorf("inserted_count = %d, want 2", bw.InsertedCount)
	}
	if bw.MatchedCount != 1 {
		t.Errorf("matched_count = %d, want 1", bw.MatchedCount)
	}
	if bw.ModifiedCount != 1 {
		t.Errorf("modified_count = %d, want 1", bw.ModifiedCount)
	}
	if bw.DeletedCount != 1 {
		t.Errorf("deleted_count = %d, want 1", bw.DeletedCount)
	}
	if len(bw.InsertedIDs) != 2 {
		t.Errorf("inserted_ids map len = %d, want 2", len(bw.InsertedIDs))
	}

	cnt, _ := DecodeData[CountData](resp.Results[1])
	if cnt.Count != 2 {
		t.Errorf("count after bulk = %d, want 2 (seed updated, 2 inserts, 1 delete)", cnt.Count)
	}
}

func TestConformance_CountDocuments_WithFilter(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	var docs []Doc
	for i := 0; i < 8; i++ {
		docs = append(docs, JSON(fmt.Sprintf(`{"n": %d}`, i)))
	}
	if _, _, err := c.Ops(Ctx(t), OpsRequest{Database: db, Ops: []Op{InsertMany("nums", docs)}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops:      []Op{CountDocuments("nums", JSON(`{"n": {"$gte": 5}}`))},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	ExpectOK(t, resp.Results[0], "countDocuments")
	cnt, _ := DecodeData[CountData](resp.Results[0])
	if cnt.Count != 3 {
		t.Errorf("count = %d, want 3", cnt.Count)
	}
}
