import threading
import requests
import time
import uuid
import json

# List of API endpoints for the nodes
NODE_URLS = [
    "http://127.0.0.1:9001/key/generate",
    "http://127.0.0.1:9002/key/generate",
    "http://127.0.0.1:9003/key/generate",
]

# List of all participating node names
PARTICIPANTS = ["node1", "node2", "node3"]

def call_generate_api(url, session_id, participants, results_list):
    """
    Sends a POST request and stores the final JSON response in a shared list.
    """
    headers = {'Content-Type': 'application/json'}
    payload = {
        'sessionId': session_id,
        'participants': participants
    }
    
    try:
        print(f"[{time.time()}] Sending request to {url}...")
        # Increase timeout because the coordinator will wait
        response = requests.post(url, headers=headers, data=json.dumps(payload), timeout=90)
        response.raise_for_status()

        response_data = response.json()
        print(f"--- Response from {url} (Status: {response.status_code}) ---")
        print(response_data)
        print("-" * 40)

        # The coordinator's response will have a final status
        if response_data.get("status") in ["Success", "Failed", "Timeout"]:
            results_list.append(response_data)

    except requests.exceptions.HTTPError as e:
        print(f"!!! HTTP Error from {url}: {e.response.status_code} {e.response.reason} !!!")
        try:
            results_list.append(e.response.json())
        except Exception:
            results_list.append({"error": str(e)})
    except requests.exceptions.RequestException as e:
        print(f"!!! Request Error calling {url}: {e} !!!")
        results_list.append({"error": str(e)})

if __name__ == "__main__":
    threads = []
    final_results = []
    session_id = str(uuid.uuid4())
    print(f"Starting key generation ceremony with Session ID: {session_id}\n")

    # Create and start a thread for each API call
    for url in NODE_URLS:
        thread = threading.Thread(target=call_generate_api, args=(url, session_id, PARTICIPANTS, final_results))
        threads.append(thread)
        thread.start()

    # Wait for all threads to complete. The script will block here until the coordinator returns.
    for thread in threads:
        thread.join()

    print("\n========================================")
    print("Key Generation Ceremony Finished.")
    print("========================================")

    if final_results:
        # Typically there will be one final result from the coordinator
        result = final_results[0]
        print(f"Final Status: {result.get('status')}")
        if result.get('status') == 'Success':
            print(f"  Session ID: {result.get('sessionId')}")
            print(f"  Key ID: {result.get('keyId')}")
        else:
            print(f"  Error: {result.get('error', 'No error details provided.')}")
    else:
        print("No definitive result received from the coordinator.")
