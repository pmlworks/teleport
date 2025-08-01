/**
 * Teleport
 * Copyright (C) 2023  Gravitational, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

import {
  MutableRefObject,
  useCallback,
  useEffect,
  useRef,
  useState,
} from 'react';

import { Attempt } from 'shared/hooks/useAttemptNext';
import { isAbortError } from 'shared/utils/abortError';

// eslint-disable-next-line no-restricted-imports -- FIXME
import { ResourcesResponse } from 'teleport/services/agents';
// eslint-disable-next-line no-restricted-imports -- FIXME
import { ApiError } from 'teleport/services/api/parseError';

/**
 * Supports fetching more data from the server when more data is available. Pass
 * a `fetchFunc` that retrieves a single batch of data. After the initial
 * request, the server is expected to return a `startKey` field that denotes the
 * next `startKey` to use for the next request.
 *
 * The hook maintains an invariant that there's only up to one valid
 * pending request at all times. Any out-of-order responses are discarded.
 */
export function useKeyBasedPagination<T>({
  fetchFunc,
  dataKey = 'agents',
  initialFetchSize = 30,
  fetchMoreSize = 20,
}: KeyBasedPaginationOptions<T>): KeyBasedPagination<T> {
  // Because we need to access the current state in `fetch`, we can't use regular
  // `useState`.
  const [stateRef, setState] = useRefState<{
    attempt: Attempt;
    finished: boolean;
    resources: T[];
    startKey: string;
  }>({
    attempt: { status: '', statusText: '' },
    finished: false,
    resources: [],
    startKey: '',
  });

  // Ephemeral state used solely to coordinate fetch calls, doesn't need to
  // cause rerenders.
  const abortController = useRef<AbortController | null>(null);
  const pendingPromise = useRef<Promise<ResourcesResponse<T>> | null>(null);

  useEffect(() => {
    // Abort a pending request when the hook unmounts.
    return () => abortController.current?.abort();
  }, []);

  const clear = useCallback(() => {
    abortController.current?.abort();
    abortController.current = null;
    pendingPromise.current = null;

    setState({
      attempt: { status: '', statusText: '' },
      startKey: '',
      finished: false,
      resources: [],
    });
  }, [setState]);

  const fetch = useCallback(
    async (options?: { force?: boolean; clear?: boolean }) => {
      if (options?.clear) {
        clear();
      }
      const { finished, attempt, resources, startKey } = stateRef.current;
      if (
        finished ||
        (!options?.force &&
          (pendingPromise.current ||
            attempt.status === 'processing' ||
            attempt.status === 'failed'))
      ) {
        return;
      }

      try {
        setState({
          ...stateRef.current,
          attempt: { status: 'processing' },
        });
        abortController.current?.abort();
        abortController.current = new AbortController();
        const limit = resources.length > 0 ? fetchMoreSize : initialFetchSize;
        const newPromise = fetchFunc(
          {
            limit,
            startKey,
          },
          abortController.current.signal
        );
        pendingPromise.current = newPromise;

        const res = await newPromise;

        if (pendingPromise.current !== newPromise) {
          return;
        }

        pendingPromise.current = null;
        abortController.current = null;

        setState({
          resources: [...resources, ...res[dataKey]],
          startKey: res.startKey,
          finished: !res.startKey,
          attempt: { status: 'success' },
        });
      } catch (err) {
        // Aborting is not really an error here.
        if (isAbortError(err)) {
          setState({
            ...stateRef.current,
            attempt: { status: '', statusText: '' },
          });
          return;
        }
        let statusCode: number | undefined;
        if (err instanceof ApiError && err.response) {
          statusCode = err.response.status;
        }
        setState({
          ...stateRef.current,
          attempt: { status: 'failed', statusText: err.message, statusCode },
        });
      }
    },
    [
      fetchFunc,
      stateRef,
      clear,
      setState,
      fetchMoreSize,
      initialFetchSize,
      dataKey,
    ]
  );

  function updateFetchedResources(modifiedResources: T[]) {
    setState({
      ...stateRef.current,
      resources: modifiedResources,
    });
  }

  return {
    fetch,
    clear,
    attempt: stateRef.current.attempt,
    resources: stateRef.current.resources,
    finished: stateRef.current.finished,
    updateFetchedResources,
  };
}

/**
 *  `useRefState` returns a mutable ref object and an update function
 *  that triggers re-render.
 */
function useRefState<T>(
  initialState: T
): [MutableRefObject<T>, (newState: T) => void] {
  const stateRef = useRef<T>(initialState);
  const [, setRefresh] = useState({});

  const setStateAndRefresh = useCallback((newState: T) => {
    stateRef.current = newState;
    setRefresh({}); // triggers re-render
  }, []);

  return [stateRef, setStateAndRefresh];
}

export type KeyBasedPaginationOptions<T> = {
  fetchFunc: (
    paginationParams: { limit: number; startKey: string },
    signal?: AbortSignal
  ) => Promise<ResourcesResponse<T>>;
  initialFetchSize?: number;
  fetchMoreSize?: number;
  dataKey?: string;
};

type KeyBasedPagination<T> = {
  /**
   * Attempts to fetch a new batch of data, unless one is already being fetched,
   * or the previous fetch resulted with an error. It is intended to be called
   * as a mere suggestion to fetch more data and can be called multiple times,
   * for example, when the user scrolls to the bottom of the page.
   *
   * @param options - Options to control the fetch behavior.
   * @param options.force - If true, cancels any pending request and
   * disregards whether an error occurred previously. This option is intended for
   * explicit user actions. Do not call it from `useInfiniteScroll` to avoid
   * flooding the server with requests.
   * @param options.clear - If true, cancels any pending request and clears
   * the state (useful for fetching data from the beginning). Similarly to
   * `force`, do not call it from `useInfiniteScroll` to avoid flooding
   * the server with requests.
   */
  fetch(options?: { force?: boolean; clear?: boolean }): Promise<void>;
  /** Aborts a pending request and clears the state. **/
  clear(): void;
  attempt: Attempt;
  resources: T[];
  finished: boolean;
  /**
   * Used in conjunction with create/delete/update operations
   * where changes are not propagated right away (from backend caching),
   * so the frontend will modify the fetched resources in place
   * instead of "re-fetching" data that can be stale.
   */
  updateFetchedResources(modifiedResources: T[]): void;
};
