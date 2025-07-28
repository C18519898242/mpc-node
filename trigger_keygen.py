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

def call_generate_api(url, session_id):
    """
    Sends a POST request with a session ID to the specified URL.
    """
    headers = {'Content-Type': 'application/json'}
    payload = {'sessionId': session_id}
    
    try:
        print(f"[{time.time()}] Sending request to {url} with session {session_id}...")
        response = requests.post(url, headers=headers, data=json.dumps(payload), timeout=60)
        response.raise_for_status()  # Raise an exception for bad status codes (4xx or 5xx)

        print(f"--- SUCCESS from {url} (Status: {response.status_code}) ---")
        print(response.json())
        print("-" * 40)

    except requests.exceptions.HTTPError as e:
        print(f"!!! HTTP Error from {url}: {e.response.status_code} {e.response.reason} !!!")
        try:
            print(f"    Error Body: {e.response.json()}")
        except Exception:
            pass
    except requests.exceptions.RequestException as e:
        print(f"!!! Request Error calling {url}: {e} !!!")

if __name__ == "__main__":
    threads = []
    session_id = str(uuid.uuid4())
    print(f"Starting key generation ceremony with Session ID: {session_id}")

    # Create and start a thread for each API call
    for url in NODE_URLS:
        thread = threading.Thread(target=call_generate_api, args=(url, session_id))
        threads.append(thread)
        thread.start()

    # Wait for all threads to complete
    for thread in threads:
        thread.join()

    print("All key generation API calls have been sent.")
