#!/bin/bash
expectedScore=1.0
echo "starting mutation tests expected score at least: ${expectedScore}"
testsOutput=$(go-mutesting controllers)
actualScore=$(echo "$testsOutput" | grep "The mutation score is" | awk '{print $5}')
if (( $(echo "$actualScore >= $expectedScore" |bc -l) )); then
  echo "mutation tests passed with score: ${actualScore}"
	exit 0
fi
echo "$testsOutput"
echo "mutation tests failed expectedScore: ${expectedScore} , actualScore: ${actualScore}"
exit 1