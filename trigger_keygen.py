import threading
import requests
import time

# List of API endpoints for the nodes
NODE_URLS = [
    "http://127.0.0.1:9001/key/generate",
    "http://127.0.0.1:9002/key/generate",
    "http://127.0.0.1:9003/key/generate",
]

def call_generate_api(url):
    """
    Sends a POST request to the specified URL and prints the response.
    """
    try:
        print(f"[{time.time()}] Sending request to {url}...")
        response = requests.post(url, timeout=60) # Increased timeout for long-running TSS process
        print(f"--- Response from {url} ---")
        print(f"Status Code: {response.status_code}")
        print(f"Body: {response.json()}")
        print("-" * 30)
    except requests.exceptions.RequestException as e:
        print(f"!!! Error calling {url}: {e} !!!")

if __name__ == "__main__":
    threads = []
    print("Starting key generation ceremony by calling all nodes...")

    # Create and start a thread for each API call
    for url in NODE_URLS:
        thread = threading.Thread(target=call_generate_api, args=(url,))
        threads.append(thread)
        thread.start()

    # Wait for all threads to complete
    for thread in threads:
        thread.join()

    print("All key generation API calls have been sent.")
