import requests
import time
import uuid
import json
import threading

# --- CONFIGURATION ---
KEYGEN_URLS = [
    "http://127.0.0.1:9001/key/generate",
    "http://127.0.0.1:9002/key/generate",
    "http://127.0.0.1:9003/key/generate",
]
SIGN_URLS = [
    "http://127.0.0.1:9001/key/sign",
    "http://127.0.0.1:9002/key/sign",
    "http://127.0.0.1:9003/key/sign",
]
PARTICIPANTS = ["node1", "node2", "node3"]
MESSAGE_TO_SIGN = "This is a test message for the fully automated TSS signing!"

def call_api(url, payload, results_list):
    """Generic function to call an API endpoint and store the result."""
    headers = {'Content-Type': 'application/json'}
    try:
        print(f"[{time.time()}] Sending request to {url}...")
        response = requests.post(url, headers=headers, data=json.dumps(payload), timeout=90)
        
        response_data = response.json()
        print(f"--- Response from {url} (Status: {response.status_code}) ---")
        print(json.dumps(response_data, indent=2))
        print("-" * 40)

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

def run_ceremony(urls, payload, ceremony_name):
    """Runs a TSS ceremony by calling multiple URLs concurrently."""
    threads = []
    final_results = []
    print(f"\n--- Starting {ceremony_name} Ceremony with Session ID: {payload['sessionId']} ---\n")

    for url in urls:
        thread = threading.Thread(target=call_api, args=(url, payload, final_results))
        threads.append(thread)
        thread.start()

    for thread in threads:
        thread.join()

    print(f"\n========================================")
    print(f"{ceremony_name} Ceremony Finished.")
    print("========================================")

    if final_results:
        result = final_results[0]
        print(f"Final Status: {result.get('status')}")
        if result.get('status') == 'Success':
            print("✅ Ceremony Successful!")
            return result
        else:
            print(f"❌ Ceremony Failed: {result.get('error', 'No error details provided.')}")
            return None
    else:
        print("No definitive result received from the coordinator. Check node logs.")
        return None

if __name__ == "__main__":
    # 1. --- Key Generation ---
    keygen_session_id = str(uuid.uuid4())
    keygen_payload = {
        'sessionId': keygen_session_id,
        'participants': PARTICIPANTS
    }
    keygen_result = run_ceremony(KEYGEN_URLS, keygen_payload, "Key Generation")

    if not keygen_result or not keygen_result.get("keyId"):
        print("\nKey generation failed or did not return a Key ID. Aborting.")
    else:
        # 2. --- Signing ---
        generated_key_id = keygen_result["keyId"]
        print(f"\nProceeding to signing with new Key ID: {generated_key_id}")
        
        sign_session_id = str(uuid.uuid4())
        sign_payload = {
            'sessionId': sign_session_id,
            'keyId': generated_key_id,
            'message': MESSAGE_TO_SIGN,
            'participants': PARTICIPANTS
        }
        sign_result = run_ceremony(SIGN_URLS, sign_payload, "Signing")
        
        if sign_result:
            print("\n--- Final Signing Result ---")
            print(f"Signature Placeholder: {sign_result.get('signature')}")
