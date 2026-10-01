#!/bin/bash
# Read-only: Writing module readiness across ALL professions.
set -u
U=$(docker exec oet-postgres printenv POSTGRES_USER)
D=$(docker exec oet-postgres printenv POSTGRES_DB)
Q() { docker exec -i oet-postgres psql -U "$U" -d "$D" -A -F' | ' -c "$1" 2>&1; }

echo "############ 1. WRITING SCENARIOS BY PROFESSION (all rows) ############"
Q "SELECT \"Profession\", \"Status\", count(*) AS n FROM \"WritingScenarios\" GROUP BY 1,2 ORDER BY 1,2;"

echo
echo "############ 2. PUBLISHED/AVAILABLE SCENARIOS PER PROFESSION ############"
Q "SELECT \"Profession\", count(*) AS total, count(*) FILTER (WHERE \"Status\"='published') AS published FROM \"WritingScenarios\" GROUP BY 1 ORDER BY 1;"

echo
echo "############ 3. MODEL ANSWER READINESS (is a reference answer attached?) ############"
Q "SELECT column_name FROM information_schema.columns WHERE table_name='WritingScenarios' ORDER BY ordinal_position;"

echo
echo "############ 4. SUBMISSIONS: status distribution, last 30 days ############"
Q "SELECT \"Status\", count(*) AS n, max(\"CreatedAt\") AS latest FROM \"WritingSubmissions\" WHERE \"CreatedAt\" > now() - interval '30 days' GROUP BY 1 ORDER BY 2 DESC;"

echo
echo "############ 5. ANY SUCCESSFUL GRADE EVER? ############"
Q "SELECT count(*) AS grades_total, max(\"CreatedAt\") AS last_grade FROM \"WritingGrades\";"

echo
echo "############ 6. GRADES IN LAST 7 DAYS (is grading live right now?) ############"
Q "SELECT count(*) AS grades_7d FROM \"WritingGrades\" WHERE \"CreatedAt\" > now() - interval '7 days';"

echo
echo "############ 7. RECENT AI USAGE: writing.grade outcomes (last 72h) ############"
Q "SELECT \"FeatureCode\", \"Outcome\", \"ErrorCode\", count(*) AS n, max(\"CreatedAt\") AS latest FROM \"AiUsageRecords\" WHERE \"FeatureCode\" LIKE 'writing%' AND \"CreatedAt\" > now() - interval '72 hours' GROUP BY 1,2,3 ORDER BY 5 DESC LIMIT 20;"

echo
echo "############ 8. AI PROVIDER HEALTH (which are usable for text chat?) ############"
Q "SELECT \"Code\",\"Dialect\",\"Category\",\"IsActive\",\"FailoverPriority\", CASE WHEN \"EncryptedApiKey\" IS NULL OR \"EncryptedApiKey\"='' THEN 'NO-KEY' ELSE 'has-key' END AS key_state FROM \"AiProviders\" WHERE \"Category\"=0 ORDER BY \"FailoverPriority\";"

echo
echo "############ 9. PROFESSIONS PRESENT IN PUBLISHED CONTENT ############"
Q "SELECT \"Profession\", count(*) AS published_scenarios FROM \"WritingScenarios\" WHERE \"Status\"='published' GROUP BY 1 ORDER BY 2 DESC;"
