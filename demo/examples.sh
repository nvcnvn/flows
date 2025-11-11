#!/bin/bash
# Loan Application Demo - Example API Calls
# This script demonstrates the complete loan application workflow
#
# API Structure:
#   All endpoints use base workflow names (e.g., "loan-application")
#   Sharding is handled automatically via consistent hashing
#   Format: /api/loans/{workflowName}/{workflowID}

set -e

API_URL="${API_URL:-http://localhost:8081}"
WORKFLOW_ID=""
WORKFLOW_NAME=""

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== Flows Demo: Loan Application Workflow ===${NC}\n"

# Step 1: Create a loan application
echo -e "${GREEN}Step 1: Starting loan application...${NC}"
RESPONSE=$(curl -s -X POST "$API_URL/api/loans" \
  -H "Content-Type: application/json" \
  -d '{
    "applicant_name": "Jane Smith",
    "amount": 75000,
    "purpose": "home renovation"
  }')

echo "$RESPONSE" | jq .
WORKFLOW_ID=$(echo "$RESPONSE" | jq -r '.workflow_id')
WORKFLOW_NAME=$(echo "$RESPONSE" | jq -r '.workflow_name')

if [ "$WORKFLOW_ID" == "null" ] || [ "$WORKFLOW_ID" == "" ]; then
  echo -e "${YELLOW}Failed to start workflow. Is the API running?${NC}"
  exit 1
fi

echo -e "${BLUE}Workflow ID: $WORKFLOW_ID${NC}"
echo -e "${BLUE}Workflow Name (Base): $WORKFLOW_NAME${NC}"
echo -e "${BLUE}Note: Sharding happens automatically - you always use the base name${NC}\n"

# Wait for credit check
echo -e "${GREEN}Waiting 2 seconds for credit check to complete...${NC}"
sleep 2

# Step 2: Check status
echo -e "\n${GREEN}Step 2: Checking workflow status...${NC}"
curl -s "$API_URL/api/loans/$WORKFLOW_NAME/$WORKFLOW_ID" | jq .

# Step 3: Submit identity document
echo -e "\n${GREEN}Step 3: Submitting identity document...${NC}"
curl -s -X POST "$API_URL/api/loans/$WORKFLOW_NAME/$WORKFLOW_ID/documents" \
  -H "Content-Type: application/json" \
  -d '{
    "document_type": "identity",
    "document_id": "DL-987654321"
  }' | jq .

echo -e "${YELLOW}Waiting 1 second for document verification...${NC}"
sleep 1

# Step 4: Submit income document
echo -e "\n${GREEN}Step 4: Submitting income document...${NC}"
curl -s -X POST "$API_URL/api/loans/$WORKFLOW_NAME/$WORKFLOW_ID/documents" \
  -H "Content-Type: application/json" \
  -d '{
    "document_type": "income",
    "document_id": "W2-2023-456"
  }' | jq .

echo -e "${YELLOW}Waiting 1 second for document verification...${NC}"
sleep 1

# Step 5: Submit address document (required for amounts > $50k)
echo -e "\n${GREEN}Step 5: Submitting address document...${NC}"
curl -s -X POST "$API_URL/api/loans/$WORKFLOW_NAME/$WORKFLOW_ID/documents" \
  -H "Content-Type: application/json" \
  -d '{
    "document_type": "address",
    "document_id": "UTIL-202401-789"
  }' | jq .

# Wait for compliance check (5 second sleep in workflow)
echo -e "\n${YELLOW}Waiting 6 seconds for compliance check (workflow sleeps for 5 seconds)...${NC}"
sleep 6

# Step 6: Check if manager approval is needed (30% random chance)
echo -e "\n${GREEN}Step 6: Checking if manager approval is needed...${NC}"
STATUS=$(curl -s "$API_URL/api/loans/$WORKFLOW_NAME/$WORKFLOW_ID")
echo "$STATUS" | jq .

# If workflow is still running, it might need manager approval
if echo "$STATUS" | jq -e '.Status == "running"' > /dev/null; then
  echo -e "\n${GREEN}Step 7: Sending manager approval (in case it's needed)...${NC}"
  curl -s -X POST "$API_URL/api/loans/$WORKFLOW_NAME/$WORKFLOW_ID/approve" \
    -H "Content-Type: application/json" \
    -d '{
      "approver_role": "manager",
      "approved": true,
      "comments": "Application looks good, all documents verified"
    }' | jq .
fi

# Wait for final processing
echo -e "\n${YELLOW}Waiting 3 seconds for final processing...${NC}"
sleep 3

# Step 7/8: Get final result
echo -e "\n${GREEN}Final Step: Getting workflow result...${NC}"
curl -s "$API_URL/api/loans/$WORKFLOW_NAME/$WORKFLOW_ID/result" | jq .

echo -e "\n${BLUE}=== Demo Complete! ===${NC}\n"
echo -e "${BLUE}Database Queries to Try:${NC}"
echo -e "  -- Note: Database stores sharded name (e.g., loan-application-shard-1)"
echo -e "  SELECT * FROM workflows WHERE id = '$WORKFLOW_ID';"
echo -e "  SELECT * FROM activities WHERE workflow_id = '$WORKFLOW_ID' ORDER BY sequence_num;"
echo -e "  SELECT * FROM signals WHERE workflow_id = '$WORKFLOW_ID';"
echo -e "  SELECT * FROM timers WHERE workflow_id = '$WORKFLOW_ID';"
echo -e "  SELECT * FROM history_events WHERE workflow_id = '$WORKFLOW_ID' ORDER BY sequence_num;"
echo ""
echo -e "${BLUE}Tip:${NC} The workflow 'name' column in the database shows the sharded name,"
echo -e "     but you always use the base name ('$WORKFLOW_NAME') in API calls."
echo ""
